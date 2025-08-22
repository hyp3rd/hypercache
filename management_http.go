package hypercache

import (
	"context"
	"net"
	"time"

	"github.com/hyp3rd/ewrap"

	fiber "github.com/gofiber/fiber/v3"

	"github.com/hyp3rd/hypercache/internal/sentinel"
	"github.com/hyp3rd/hypercache/pkg/stats"
)

// ManagementHTTPOption configures the management HTTP server.
type ManagementHTTPOption func(*ManagementHTTPServer)

// ManagementHTTPServer holds Fiber app and settings.
type ManagementHTTPServer struct {
	addr             string
	app              *fiber.App
	readTimeout      time.Duration
	writeTimeout     time.Duration
	authFunc         func(fiber.Ctx) error
	ln               net.Listener
	started          bool
	listenerDeadline time.Duration
}

// WithMgmtAuth sets an auth function (return error to block).
func WithMgmtAuth(fn func(fiber.Ctx) error) ManagementHTTPOption {
	return func(s *ManagementHTTPServer) { s.authFunc = fn }
}

// WithMgmtReadTimeout sets read timeout.
func WithMgmtReadTimeout(d time.Duration) ManagementHTTPOption {
	return func(s *ManagementHTTPServer) { s.readTimeout = d }
}

// WithMgmtWriteTimeout sets write timeout.
func WithMgmtWriteTimeout(d time.Duration) ManagementHTTPOption {
	return func(s *ManagementHTTPServer) { s.writeTimeout = d }
}

const (
	defaultReadTimeout      = 5 * time.Second
	defaultWriteTimeout     = 5 * time.Second
	defaultListenerDeadline = 2 * time.Second
)

// NewManagementHTTPServer builds an HTTP server holder (lazy start).
func NewManagementHTTPServer(addr string, opts ...ManagementHTTPOption) *ManagementHTTPServer {
	app := fiber.New(fiber.Config{
		ReadTimeout:  defaultReadTimeout,
		WriteTimeout: defaultWriteTimeout,
	})

	srv := &ManagementHTTPServer{
		addr:             addr,
		app:              app,
		readTimeout:      defaultReadTimeout,
		writeTimeout:     defaultWriteTimeout,
		listenerDeadline: defaultListenerDeadline,
	}
	for _, opt := range opts { // apply options
		opt(srv)
	}

	return srv
}

// mountRoutes registers endpoints onto the Fiber app.
type managementCache interface {
	GetStats() stats.Stats
	Capacity() int
	Allocation() int64
	MaxCacheSize() int64
	TriggerEviction()
	TriggerExpiration()
	EvictionInterval() time.Duration
	ExpirationInterval() time.Duration
	EvictionAlgorithm() string
	Clear(ctx context.Context) error
}

// managementCacheDistOpt holds optional distributed introspection (queried via type assertion).
type managementCacheDistOpt interface {
	DistMetrics() any
	ClusterOwners(key string) []string
}

type membershipIntrospect interface {
	DistMembershipSnapshot() (
		members []struct {
			ID          string
			Address     string
			State       string
			Incarnation uint64
		},
		replication int,
		vnodes int,
	)
	DistRingHashSpots() []string
}

// Start launches listener (idempotent). Caller provides cache for handler wiring.
func (s *ManagementHTTPServer) Start(ctx context.Context, hc managementCache) error {
	if s.started { // idempotent
		return nil
	}

	s.mountRoutes(ctx, hc)

	lc := net.ListenConfig{}

	ln, err := lc.Listen(ctx, "tcp", s.addr)
	if err != nil {
		return ewrap.Wrap(err, "mgmt listen")
	}

	s.ln = ln

	go func() { // serve in background (optional server errors are ignored intentionally)
		err = s.app.Listener(ln)
		if err != nil { // optional server; log hook could be added in future
			_ = err
		}
	}()

	s.started = true

	return nil
}

// Address returns the bound address (useful when passing ":0" for ephemeral port). Empty if not started yet.
func (s *ManagementHTTPServer) Address() string {
	if s.ln == nil {
		return ""
	}

	return s.ln.Addr().String()
}

// Shutdown stops the server.
func (s *ManagementHTTPServer) Shutdown(ctx context.Context) error {
	if !s.started {
		return nil
	}

	ch := make(chan error, 1)

	go func() {
		ch <- s.app.Shutdown()
	}()

	select {
	case <-ctx.Done():
		return sentinel.ErrMgmtHTTPShutdownTimeout
	case err := <-ch:
		return err
	}
}

// mountRoutes.
func (s *ManagementHTTPServer) mountRoutes(ctx context.Context, hc managementCache) {
	// optional auth wrapper
	useAuth := func(handler fiber.Handler) fiber.Handler {
		if s.authFunc == nil {
			return handler
		}

		return func(fiberCtx fiber.Ctx) error {
			authErr := s.authFunc(fiberCtx)
			if authErr != nil {
				return authErr
			}

			return handler(fiberCtx)
		}
	}

	// health
	s.app.Get("/health", useAuth(func(fiberCtx fiber.Ctx) error { return fiberCtx.SendString("ok") }))
	// stats
	s.app.Get("/stats", useAuth(func(fiberCtx fiber.Ctx) error { return fiberCtx.JSON(hc.GetStats()) }))
	// distributed metrics (if available via optional interface)
	s.app.Get("/dist/metrics", useAuth(func(fiberCtx fiber.Ctx) error {
		if dist, ok := hc.(managementCacheDistOpt); ok {
			m := dist.DistMetrics()
			if m == nil {
				return fiberCtx.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "dist metrics not available"})
			}

			return fiberCtx.JSON(m)
		}

		return fiberCtx.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "distributed backend unsupported"})
	}))

	// cluster owners debug for a key (?key=foo)
	s.app.Get("/dist/owners", useAuth(func(fiberCtx fiber.Ctx) error {
		if dist, ok := hc.(managementCacheDistOpt); ok {
			key := fiberCtx.Query("key")
			if key == "" {
				return fiberCtx.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "missing key"})
			}

			owners := dist.ClusterOwners(key)

			return fiberCtx.JSON(fiber.Map{"key": key, "owners": owners})
		}

		return fiberCtx.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "distributed backend unsupported"})
	}))

	// cluster members
	s.app.Get("/cluster/members", useAuth(func(fiberCtx fiber.Ctx) error {
		if mi, ok := hc.(membershipIntrospect); ok {
			members, replication, vnodes := mi.DistMembershipSnapshot()

			return fiberCtx.JSON(fiber.Map{"replication": replication, "virtualNodes": vnodes, "members": members})
		}

		return fiberCtx.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "distributed backend unsupported"})
	}))

	// cluster ring hashes
	s.app.Get("/cluster/ring", useAuth(func(fiberCtx fiber.Ctx) error {
		if mi, ok := hc.(membershipIntrospect); ok {
			spots := mi.DistRingHashSpots()

			return fiberCtx.JSON(fiber.Map{"count": len(spots), "vnodes": spots})
		}

		return fiberCtx.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "distributed backend unsupported"})
	}))
	// config (sanitized minimal)
	s.app.Get("/config", useAuth(func(fiberCtx fiber.Ctx) error {
		cfg := map[string]any{
			"capacity":           hc.Capacity(),
			"allocation":         hc.Allocation(),
			"maxCacheSize":       hc.MaxCacheSize(),
			"evictionInterval":   hc.EvictionInterval().String(),
			"expirationInterval": hc.ExpirationInterval().String(),
			"evictionAlgorithm":  hc.EvictionAlgorithm(),
		}

		// If distributed config available, enrich response (replication, virtualNodes, nodeCount)
		if mi, ok := hc.(membershipIntrospect); ok {
			_, replication, vnodes := mi.DistMembershipSnapshot()
			cfg["replication"] = replication
			cfg["virtualNodesPerNode"] = vnodes
		}

		return fiberCtx.JSON(cfg)
	}))
	// trigger eviction
	s.app.Post("/evict", useAuth(func(fiberCtx fiber.Ctx) error {
		hc.TriggerEviction()

		return fiberCtx.SendStatus(fiber.StatusAccepted)
	}))
	// trigger expiration
	s.app.Post("/trigger-expiration", useAuth(func(fiberCtx fiber.Ctx) error {
		hc.TriggerExpiration()

		return fiberCtx.SendStatus(fiber.StatusAccepted)
	}))
	// clear cache
	s.app.Post("/clear", useAuth(func(fiberCtx fiber.Ctx) error {
		clearErr := hc.Clear(ctx)
		if clearErr != nil {
			return clearErr
		}

		return fiberCtx.SendStatus(fiber.StatusOK)
	}))
}
