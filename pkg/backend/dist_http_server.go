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
	s.registerSet(ctx, dm)
	s.registerGet(ctx, dm)
	s.registerRemove(ctx, dm)
	s.registerHealth()
	s.registerMerkle(ctx, dm)

	return s.listen(ctx)
}

func (s *distHTTPServer) registerSet(ctx context.Context, dm *DistMemory) { //nolint:ireturn
	s.app.Post("/internal/cache/set", func(fctx fiber.Ctx) error { // small handler
		var req httpSetRequest

		body := fctx.Body()

		unmarshalErr := json.Unmarshal(body, &req)
		if unmarshalErr != nil { // separated to satisfy noinlineerr
			return fctx.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": unmarshalErr.Error()})
		}

		it := &cache.Item{ // LastUpdated set to now for replicated writes
			Key:         req.Key,
			Value:       req.Value,
			Expiration:  time.Duration(req.Expiration) * time.Millisecond,
			Version:     req.Version,
			Origin:      req.Origin,
			LastUpdated: time.Now(),
		}

		dm.applySet(ctx, it, req.Replicate)

		return fctx.JSON(httpSetResponse{})
	})
}

func (s *distHTTPServer) registerGet(_ context.Context, dm *DistMemory) { //nolint:ireturn
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
}

func (s *distHTTPServer) registerRemove(ctx context.Context, dm *DistMemory) { //nolint:ireturn
	s.app.Delete("/internal/cache/remove", func(fctx fiber.Ctx) error {
		key := fctx.Query("key")
		if key == "" {
			return fctx.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "missing key"})
		}

		replicate, parseErr := strconv.ParseBool(fctx.Query("replicate", "false"))
		if parseErr != nil {
			return fctx.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid replicate"})
		}

		dm.applyRemove(ctx, key, replicate)

		return fctx.SendStatus(fiber.StatusOK)
	})
}

func (s *distHTTPServer) registerHealth() { //nolint:ireturn
	s.app.Get("/health", func(fctx fiber.Ctx) error { return fctx.SendString("ok") })
}

func (s *distHTTPServer) registerMerkle(_ context.Context, dm *DistMemory) { //nolint:ireturn
	s.app.Get("/internal/merkle", func(fctx fiber.Ctx) error {
		tree := dm.BuildMerkleTree()

		return fctx.JSON(fiber.Map{
			"root":        tree.Root,
			"leaf_hashes": tree.LeafHashes,
			"chunk_size":  tree.ChunkSize,
		})
	})

	// naive keys listing for anti-entropy (testing only). Not efficient for large datasets.
	s.app.Get("/internal/keys", func(fctx fiber.Ctx) error {
		var keys []string
		for _, shard := range dm.shards {
			if shard == nil {
				continue
			}

			ch := shard.items.IterBuffered()
			for t := range ch {
				keys = append(keys, t.Key)
			}
		}

		return fctx.JSON(fiber.Map{"keys": keys})
	})
}

func (s *distHTTPServer) listen(ctx context.Context) error { //nolint:ireturn
	lc := net.ListenConfig{}

	ln, err := lc.Listen(ctx, "tcp", s.addr)
	if err != nil {
		return ewrap.Wrap(err, "dist http listen")
	}

	s.ln = ln

	go func() { // capture server errors (ignored intentionally for now)
		serveErr := s.app.Listener(ln)
		if serveErr != nil { // separated for noinlineerr linter
			_ = serveErr
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
