package backend

import (
	"context"
	"net"
	"strconv"
	"time"

	"github.com/goccy/go-json"

	"github.com/hyp3rd/ewrap"

	fiber "github.com/gofiber/fiber/v3"

	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

type distHTTPServer struct {
	app  *fiber.App
	ln   net.Listener
	addr string
}

// minimal request/response types reused by transport
// request/response DTOs defined in dist_http_types.go

const (
	httpReadTimeout  = 5 * time.Second
	httpWriteTimeout = 5 * time.Second
)

func newDistHTTPServer(addr string) *distHTTPServer {
	app := fiber.New(fiber.Config{ReadTimeout: httpReadTimeout, WriteTimeout: httpWriteTimeout})

	return &distHTTPServer{app: app, addr: addr}
}

func (s *distHTTPServer) start(ctx context.Context, dm *DistMemory) error { //nolint:ireturn
	// routes
	// set
	// POST /internal/cache/set
	// body: httpSetRequest
	s.app.Post("/internal/cache/set", func(fctx fiber.Ctx) error {
		var req httpSetRequest

		err := json.Unmarshal(fctx.Body(), &req)
		if err != nil {
			return fctx.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}

		it := &cache.Item{Key: req.Key, Value: req.Value, Expiration: time.Duration(req.Expiration) * time.Millisecond, Version: req.Version, Origin: req.Origin}
		if req.Replicate {
			dm.applySet(ctx, it, true)

			return fctx.JSON(httpSetResponse{})
		}

		dm.applySet(ctx, it, false)

		return fctx.JSON(httpSetResponse{})
	})

	s.app.Get("/internal/cache/get", func(fctx fiber.Ctx) error {
		key := fctx.Query("key")
		if key == "" {
			return fctx.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "missing key"})
		}

		owners := dm.lookupOwners(key)
		if len(owners) == 0 {
			return fctx.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "not owner"})
		}

		if it, ok := dm.shardFor(key).items.Get(key); ok {
			return fctx.JSON(httpGetResponse{Found: true, Item: it})
		}

		return fctx.JSON(httpGetResponse{Found: false})
	})

	s.app.Delete("/internal/cache/remove", func(fctx fiber.Ctx) error {
		key := fctx.Query("key")
		if key == "" {
			return fctx.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "missing key"})
		}

		replicateRaw := fctx.Query("replicate", "false")

		replicate, parseErr := strconv.ParseBool(replicateRaw)
		if parseErr != nil {
			return fctx.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid replicate"})
		}

		dm.applyRemove(ctx, key, replicate)

		return fctx.SendStatus(fiber.StatusOK)
	})

	// reuse /health in management server or provide inline
	s.app.Get("/health", func(fctx fiber.Ctx) error {
		return fctx.SendString("ok")
	})

	lc := net.ListenConfig{}

	ln, err := lc.Listen(ctx, "tcp", s.addr)
	if err != nil {
		return ewrap.Wrap(err, "dist http listen")
	}

	s.ln = ln

	go func() {
		err = s.app.Listener(ln)
		if err != nil {
			return
		}
	}()

	return nil
}

func (s *distHTTPServer) stop(ctx context.Context) error { //nolint:ireturn
	if s == nil || s.ln == nil {
		return nil
	}

	ch := make(chan error, 1)

	go func() { ch <- s.app.Shutdown() }()

	select {
	case <-ctx.Done():
		return ewrap.Newf("http server shutdown timeout")
	case err := <-ch:
		return err
	}
}
