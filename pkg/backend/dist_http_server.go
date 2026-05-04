package backend

import (
	"context"
	"net"
	"strconv"
	"time"

	"github.com/goccy/go-json"
	fiber "github.com/gofiber/fiber/v3"
	"github.com/hyp3rd/ewrap"

	"github.com/hyp3rd/hypercache/internal/constants"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

type distHTTPServer struct {
	app  *fiber.App
	ln   net.Listener
	addr string
	// ctx is the server-lifecycle context captured at start. Handlers
	// pass it to backend operations so cancellation propagates when the
	// caller cancels the constructor ctx (e.g. via DistMemory Stop).
	//
	// We do NOT use the per-request fiber.Ctx for this: fiber.Ctx is
	// pooled and reset after the handler returns, which races with
	// happy-eyeballs goroutines spawned by net.(*Dialer).DialContext
	// when applySet's replica fan-out goes through http.Client.Do.
	// See the race trace captured during phase-5b investigation.
	ctx context.Context //nolint:containedctx // captured server lifecycle, not request scope
}

// minimal request/response types reused by transport
// request/response DTOs defined in dist_http_types.go

const (
	httpReadTimeout  = 5 * time.Second
	httpWriteTimeout = 5 * time.Second

	// defaultDistHTTPBodyLimit caps inbound request bodies the dist HTTP
	// server will accept. 16 MiB is generous for typical cache values
	// while still rejecting absurd payloads. Tunable via
	// WithDistHTTPLimits.
	defaultDistHTTPBodyLimit = 16 * 1024 * 1024
	// defaultDistHTTPResponseLimit caps inbound response bodies the dist
	// HTTP client will accept. Mirrors BodyLimit so a peer cannot OOM
	// the requester via an oversized response.
	defaultDistHTTPResponseLimit int64 = 16 * 1024 * 1024
	// defaultDistHTTPIdleTimeout is the keep-alive idle timeout. Without
	// it idle connections accumulate; fiber's default is unbounded.
	defaultDistHTTPIdleTimeout = 60 * time.Second
	// defaultDistHTTPConcurrency caps simultaneous in-flight handlers.
	// Matches fiber's own default but stated explicitly so it shows up in
	// /config introspection.
	defaultDistHTTPConcurrency = 256 * 1024
	// defaultDistHTTPClientTimeout is the per-request deadline for the
	// dist HTTP client when the caller doesn't supply one. 5s aligns with
	// server read/write timeouts; the previous 2s caused flakes under
	// -race when the fiber listener was slow to accept the first request.
	defaultDistHTTPClientTimeout = 5 * time.Second
)

// DistHTTPLimits bundles the tunable HTTP-transport limits applied to both
// the dist HTTP server (inbound request bodies, timeouts, concurrency) and
// the auto-created dist HTTP client (outbound request timeout, inbound
// response size). Zero-valued fields fall back to the defaults below.
//
// Use [WithDistHTTPLimits] to override defaults; partial overrides keep
// the rest at their default values.
type DistHTTPLimits struct {
	// BodyLimit caps inbound request body bytes (server-side).
	BodyLimit int
	// ResponseLimit caps inbound response body bytes (client-side).
	ResponseLimit int64
	// ReadTimeout is the server read deadline.
	ReadTimeout time.Duration
	// WriteTimeout is the server write deadline.
	WriteTimeout time.Duration
	// IdleTimeout is the keep-alive idle timeout (server-side).
	IdleTimeout time.Duration
	// Concurrency is the maximum number of concurrent in-flight handlers.
	Concurrency int
	// ClientTimeout is the per-request deadline for the dist HTTP client.
	ClientTimeout time.Duration
}

// withDefaults fills any zero-valued field on l with the package default.
// Returned by value — callers should treat the result as immutable.
func (l DistHTTPLimits) withDefaults() DistHTTPLimits {
	if l.BodyLimit <= 0 {
		l.BodyLimit = defaultDistHTTPBodyLimit
	}

	if l.ResponseLimit <= 0 {
		l.ResponseLimit = defaultDistHTTPResponseLimit
	}

	if l.ReadTimeout <= 0 {
		l.ReadTimeout = httpReadTimeout
	}

	if l.WriteTimeout <= 0 {
		l.WriteTimeout = httpWriteTimeout
	}

	if l.IdleTimeout <= 0 {
		l.IdleTimeout = defaultDistHTTPIdleTimeout
	}

	if l.Concurrency <= 0 {
		l.Concurrency = defaultDistHTTPConcurrency
	}

	if l.ClientTimeout <= 0 {
		l.ClientTimeout = defaultDistHTTPClientTimeout
	}

	return l
}

func newDistHTTPServer(addr string, limits DistHTTPLimits) *distHTTPServer {
	limits = limits.withDefaults()

	app := fiber.New(fiber.Config{
		ReadTimeout:  limits.ReadTimeout,
		WriteTimeout: limits.WriteTimeout,
		IdleTimeout:  limits.IdleTimeout,
		BodyLimit:    limits.BodyLimit,
		Concurrency:  limits.Concurrency,
	})

	return &distHTTPServer{app: app, addr: addr}
}

func (s *distHTTPServer) start(ctx context.Context, dm *DistMemory) error {
	s.ctx = ctx
	s.registerSet(dm)
	s.registerGet(dm)
	s.registerRemove(dm)
	s.registerHealth()
	s.registerMerkle(dm)

	return s.listen(ctx)
}

// handleSet decodes a httpSetRequest and applies it locally + optionally
// fan-outs to replicas. Uses s.ctx (server-lifecycle) as the backend
// operation context — see the comment on distHTTPServer.ctx for why we
// can't use the per-request fiber.Ctx here.
func (s *distHTTPServer) handleSet(fctx fiber.Ctx, dm *DistMemory) error {
	var req httpSetRequest

	unmarshalErr := json.Unmarshal(fctx.Body(), &req)
	if unmarshalErr != nil { // separated to satisfy noinlineerr
		return fctx.Status(fiber.StatusBadRequest).JSON(fiber.Map{constants.ErrorLabel: unmarshalErr.Error()})
	}

	it := &cache.Item{ // LastUpdated set to now for replicated writes
		Key:         req.Key,
		Value:       req.Value,
		Expiration:  time.Duration(req.Expiration) * time.Millisecond,
		Version:     req.Version,
		Origin:      req.Origin,
		LastUpdated: time.Now(),
	}

	dm.applySet(s.ctx, it, req.Replicate)

	return fctx.JSON(httpSetResponse{})
}

func (s *distHTTPServer) registerSet(dm *DistMemory) {
	handler := func(fctx fiber.Ctx) error { return s.handleSet(fctx, dm) }
	// legacy + canonical paths share the same handler.
	s.app.Post("/internal/cache/set", handler)
	s.app.Post("/internal/set", handler)
}

// handleGet looks up a key locally for a remote owner that ring-routed
// to this node. Get itself is synchronous and doesn't take a ctx, so this
// handler doesn't need one.
func (*distHTTPServer) handleGet(fctx fiber.Ctx, dm *DistMemory) error {
	key := fctx.Query("key")
	if key == "" {
		return fctx.Status(fiber.StatusBadRequest).JSON(fiber.Map{constants.ErrorLabel: constants.ErrMsgMissingCacheKey})
	}

	owners := dm.lookupOwners(key)
	if len(owners) == 0 {
		return fctx.Status(fiber.StatusNotFound).JSON(fiber.Map{constants.ErrorLabel: "not owner"})
	}

	if it, ok := dm.shardFor(key).items.Get(key); ok {
		return fctx.JSON(httpGetResponse{Found: true, Item: it})
	}

	return fctx.JSON(httpGetResponse{Found: false})
}

func (s *distHTTPServer) registerGet(dm *DistMemory) {
	handler := func(fctx fiber.Ctx) error { return s.handleGet(fctx, dm) }
	s.app.Get("/internal/cache/get", handler)
	s.app.Get("/internal/get", handler)
}

// handleRemove deletes a key locally and optionally fan-outs the delete
// to replicas. Uses s.ctx for backend operations — see the comment on
// distHTTPServer.ctx for why per-request fiber.Ctx is unsafe here.
func (s *distHTTPServer) handleRemove(fctx fiber.Ctx, dm *DistMemory) error {
	key := fctx.Query("key")
	if key == "" {
		return fctx.Status(fiber.StatusBadRequest).JSON(fiber.Map{constants.ErrorLabel: constants.ErrMsgMissingCacheKey})
	}

	replicate, parseErr := strconv.ParseBool(fctx.Query("replicate", "false"))
	if parseErr != nil {
		return fctx.Status(fiber.StatusBadRequest).JSON(fiber.Map{constants.ErrorLabel: "invalid replicate"})
	}

	dm.applyRemove(s.ctx, key, replicate)

	return fctx.SendStatus(fiber.StatusOK)
}

func (s *distHTTPServer) registerRemove(dm *DistMemory) {
	handler := func(fctx fiber.Ctx) error { return s.handleRemove(fctx, dm) }
	s.app.Delete("/internal/cache/remove", handler)
	s.app.Delete("/internal/del", handler)
}

func (s *distHTTPServer) registerHealth() {
	s.app.Get("/health", func(fctx fiber.Ctx) error { return fctx.SendString("ok") })
}

func (s *distHTTPServer) registerMerkle(dm *DistMemory) {
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

			for k := range shard.items.All() {
				keys = append(keys, k)
			}
		}

		return fctx.JSON(fiber.Map{"keys": keys})
	})
}

func (s *distHTTPServer) listen(ctx context.Context) error {
	lc := net.ListenConfig{}

	ln, err := lc.Listen(ctx, "tcp", s.addr)
	if err != nil {
		return ewrap.Wrap(err, "dist http listen")
	}

	s.ln = ln

	go func() { // capture server errors (ignored intentionally for now)
		// DisableStartupMessage avoids fiber's per-instance banner spam,
		// which would otherwise flood test output at -count=N (see hundreds of
		// "INFO Server started on..." lines drowning real failures).
		serveErr := s.app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true})
		if serveErr != nil { // separated for noinlineerr linter
			_ = serveErr
		}
	}()

	return nil
}

func (s *distHTTPServer) stop(ctx context.Context) error {
	if s == nil || s.ln == nil {
		return nil
	}

	// ShutdownWithContext closes listeners gracefully, waits for in-flight
	// requests, and force-closes once ctx's deadline elapses — replacing
	// the previous `go func() { ch <- s.app.Shutdown() }()` pattern which
	// leaked the shutdown goroutine when our ctx fired before fiber
	// finished draining.
	return s.app.ShutdownWithContext(ctx)
}
