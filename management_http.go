package hypercache

import (
	"context"
	"net"
	"time"

	fiber "github.com/gofiber/fiber/v3"
	"github.com/hyp3rd/ewrap"

	"github.com/hyp3rd/hypercache/internal/constants"
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
	idleTimeout      time.Duration
	bodyLimit        int
	concurrency      int
	authFunc         func(fiber.Ctx) error
	ln               net.Listener
	started          bool
	listenerDeadline time.Duration
	// ctx is the server-lifecycle context captured at Start. Handlers
	// pass it to backend operations (Clear in particular) so cancellation
	// propagates from HyperCache.Stop. We do NOT use the per-request
	// fiber.Ctx for this: fiber.Ctx is pooled and reset after the handler
	// returns, racing with happy-eyeballs goroutines spawned by
	// net.(*Dialer).DialContext when DistMemory's transport fan-out goes
	// through http.Client.Do.
	ctx context.Context //nolint:containedctx // captured server lifecycle, not request scope
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

// WithMgmtIdleTimeout sets the keep-alive idle timeout. Without this idle
// connections accumulate; fiber's default is unbounded. <=0 keeps the
// package default.
func WithMgmtIdleTimeout(d time.Duration) ManagementHTTPOption {
	return func(s *ManagementHTTPServer) {
		if d > 0 {
			s.idleTimeout = d
		}
	}
}

// WithMgmtBodyLimit caps inbound request body bytes. Defaults to fiber's
// 4 MiB. <=0 keeps the package default.
func WithMgmtBodyLimit(bytes int) ManagementHTTPOption {
	return func(s *ManagementHTTPServer) {
		if bytes > 0 {
			s.bodyLimit = bytes
		}
	}
}

// WithMgmtConcurrency caps simultaneous in-flight handlers. <=0 keeps the
// package default (256 KiB, matching fiber).
func WithMgmtConcurrency(n int) ManagementHTTPOption {
	return func(s *ManagementHTTPServer) {
		if n > 0 {
			s.concurrency = n
		}
	}
}

const (
	defaultReadTimeout      = 5 * time.Second
	defaultWriteTimeout     = 5 * time.Second
	defaultListenerDeadline = 2 * time.Second
	// defaultMgmtIdleTimeout caps keep-alive idle connections.
	defaultMgmtIdleTimeout = 60 * time.Second
	// defaultMgmtBodyLimit matches fiber's own default but is stated
	// explicitly so the value is visible in /config and tunable via
	// WithMgmtBodyLimit.
	defaultMgmtBodyLimit = 4 * 1024 * 1024
	// defaultMgmtConcurrency matches fiber's own default.
	defaultMgmtConcurrency = 256 * 1024
)

// NewManagementHTTPServer builds an HTTP server holder (lazy start).
func NewManagementHTTPServer(addr string, opts ...ManagementHTTPOption) *ManagementHTTPServer {
	srv := &ManagementHTTPServer{
		addr:             addr,
		readTimeout:      defaultReadTimeout,
		writeTimeout:     defaultWriteTimeout,
		idleTimeout:      defaultMgmtIdleTimeout,
		bodyLimit:        defaultMgmtBodyLimit,
		concurrency:      defaultMgmtConcurrency,
		listenerDeadline: defaultListenerDeadline,
	}
	for _, opt := range opts { // apply options
		opt(srv)
	}

	// Construct the fiber app *after* options apply so user-supplied
	// timeouts/limits actually take effect (the previous order built the
	// app with default config, then mutated unrelated struct fields).
	srv.app = fiber.New(fiber.Config{
		ReadTimeout:  srv.readTimeout,
		WriteTimeout: srv.writeTimeout,
		IdleTimeout:  srv.idleTimeout,
		BodyLimit:    srv.bodyLimit,
		Concurrency:  srv.concurrency,
	})

	return srv
}

// mountRoutes registers endpoints onto the Fiber app.
type managementCache interface {
	GetStats() stats.Stats
	Capacity() int
	Allocation() int64
	MaxCacheSize() int64
	TriggerEviction(ctx context.Context)
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
	DistHeartbeatMetrics() any
}

// Start launches listener (idempotent). Caller provides cache for handler wiring.
func (s *ManagementHTTPServer) Start(ctx context.Context, hc managementCache) error {
	if s.started { // idempotent
		return nil
	}

	s.ctx = ctx
	s.mountRoutes(hc)

	lc := net.ListenConfig{}

	ln, err := lc.Listen(ctx, "tcp", s.addr)
	if err != nil {
		return ewrap.Wrap(err, "mgmt listen")
	}

	s.ln = ln

	go func() { // serve in background (optional server errors are ignored intentionally)
		// Suppress fiber's startup banner so tests at -count=N do not drown
		// real failures under hundreds of "INFO Server started on..." lines.
		err = s.app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true})
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

	// ShutdownWithContext closes listeners gracefully, waits for in-flight
	// requests, and force-closes once ctx's deadline elapses. Replaces
	// the previous go-routine + select pattern that leaked the shutdown
	// goroutine when our ctx fired first.
	return s.app.ShutdownWithContext(ctx)
}

// mountRoutes.
func (s *ManagementHTTPServer) mountRoutes(hc managementCache) { // split into helpers to satisfy funlen
	useAuth := s.wrapAuth
	s.registerBasic(useAuth, hc)
	s.registerDistributed(useAuth, hc)
	s.registerCluster(useAuth, hc)
	s.registerControl(useAuth, hc)
}

// wrapAuth returns an auth-wrapped handler if authFunc provided.
func (s *ManagementHTTPServer) wrapAuth(handler fiber.Handler) fiber.Handler {
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

func (s *ManagementHTTPServer) registerBasic(useAuth func(fiber.Handler) fiber.Handler, hc managementCache) {
	s.app.Get("/health", useAuth(func(fiberCtx fiber.Ctx) error { return fiberCtx.SendString("ok") }))
	s.app.Get("/stats", useAuth(func(fiberCtx fiber.Ctx) error { return fiberCtx.JSON(hc.GetStats()) }))
	s.app.Get("/config", useAuth(func(fiberCtx fiber.Ctx) error {
		cfg := map[string]any{
			"capacity":           hc.Capacity(),
			"allocation":         hc.Allocation(),
			"maxCacheSize":       hc.MaxCacheSize(),
			"evictionInterval":   hc.EvictionInterval().String(),
			"expirationInterval": hc.ExpirationInterval().String(),
			"evictionAlgorithm":  hc.EvictionAlgorithm(),
		}

		if mi, ok := hc.(membershipIntrospect); ok { // enrich distributed
			_, replication, vnodes := mi.DistMembershipSnapshot()

			cfg["replication"] = replication
			cfg["virtualNodesPerNode"] = vnodes
		}

		return fiberCtx.JSON(cfg)
	}))
}

func (s *ManagementHTTPServer) registerDistributed(useAuth func(fiber.Handler) fiber.Handler, hc managementCache) {
	s.app.Get("/dist/metrics", useAuth(func(fiberCtx fiber.Ctx) error {
		if dist, ok := hc.(managementCacheDistOpt); ok {
			m := dist.DistMetrics()
			if m == nil {
				return fiberCtx.Status(fiber.StatusNotFound).JSON(fiber.Map{constants.ErrorLabel: "dist metrics not available"})
			}

			return fiberCtx.JSON(m)
		}

		return fiberCtx.Status(fiber.StatusNotFound).JSON(fiber.Map{constants.ErrorLabel: constants.ErrMsgUnsupportedDistributedBackend})
	}))
	s.app.Get("/dist/owners", useAuth(func(fiberCtx fiber.Ctx) error {
		if dist, ok := hc.(managementCacheDistOpt); ok {
			key := fiberCtx.Query("key")
			if key == "" {
				return fiberCtx.Status(fiber.StatusBadRequest).JSON(fiber.Map{constants.ErrorLabel: constants.ErrMsgMissingCacheKey})
			}

			owners := dist.ClusterOwners(key)

			return fiberCtx.JSON(fiber.Map{"key": key, "owners": owners})
		}

		return fiberCtx.Status(fiber.StatusNotFound).JSON(fiber.Map{constants.ErrorLabel: constants.ErrMsgUnsupportedDistributedBackend})
	}))
}

func (s *ManagementHTTPServer) registerCluster(useAuth func(fiber.Handler) fiber.Handler, hc managementCache) {
	s.app.Get("/cluster/members", useAuth(func(fiberCtx fiber.Ctx) error {
		if mi, ok := hc.(membershipIntrospect); ok {
			members, replication, vnodes := mi.DistMembershipSnapshot()

			return fiberCtx.JSON(fiber.Map{"replication": replication, "virtualNodes": vnodes, "members": members})
		}

		return fiberCtx.Status(fiber.StatusNotFound).JSON(fiber.Map{constants.ErrorLabel: constants.ErrMsgUnsupportedDistributedBackend})
	}))
	s.app.Get("/cluster/ring", useAuth(func(fiberCtx fiber.Ctx) error {
		if mi, ok := hc.(membershipIntrospect); ok {
			spots := mi.DistRingHashSpots()

			return fiberCtx.JSON(fiber.Map{"count": len(spots), "vnodes": spots})
		}

		return fiberCtx.Status(fiber.StatusNotFound).JSON(fiber.Map{constants.ErrorLabel: constants.ErrMsgUnsupportedDistributedBackend})
	}))
	s.app.Get("/cluster/heartbeat", useAuth(func(fiberCtx fiber.Ctx) error { // heartbeat metrics
		if mi, ok := hc.(membershipIntrospect); ok {
			return fiberCtx.JSON(mi.DistHeartbeatMetrics())
		}

		return fiberCtx.Status(fiber.StatusNotFound).JSON(fiber.Map{constants.ErrorLabel: constants.ErrMsgUnsupportedDistributedBackend})
	}))
}

func (s *ManagementHTTPServer) registerControl(
	useAuth func(fiber.Handler) fiber.Handler,
	hc managementCache,
) {
	// Handlers use s.ctx (server-lifecycle) for backend ops. Per-request
	// fiber.Ctx would race when Clear's transport fan-out spawns
	// happy-eyeballs dial goroutines that outlive the handler — see the
	// comment on ManagementHTTPServer.ctx for the trace.
	s.app.Post("/evict", useAuth(func(fiberCtx fiber.Ctx) error {
		hc.TriggerEviction(s.ctx)

		return fiberCtx.SendStatus(fiber.StatusAccepted)
	}))
	s.app.Post("/trigger-expiration", useAuth(func(fiberCtx fiber.Ctx) error {
		hc.TriggerExpiration()

		return fiberCtx.SendStatus(fiber.StatusAccepted)
	}))
	s.app.Post("/clear", useAuth(func(fiberCtx fiber.Ctx) error {
		clearErr := hc.Clear(s.ctx)
		if clearErr != nil {
			return clearErr
		}

		return fiberCtx.SendStatus(fiber.StatusOK)
	}))
}
