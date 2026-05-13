// Command hypercache-server runs a single HyperCache node configured
// for the distributed in-memory backend (DistMemory). It exposes three
// HTTP listeners:
//
//   - Client REST API on HYPERCACHE_API_ADDR (default :8080) — apps
//     PUT/GET/DELETE keys here.
//   - Management HTTP on HYPERCACHE_MGMT_ADDR (default :8081) — admin
//     and observability endpoints (/health, /stats, /config,
//     /dist/metrics, /cluster/*).
//   - Dist HTTP on HYPERCACHE_DIST_ADDR (default :7946) — peer-to-peer
//     replication, anti-entropy, and heartbeat.
//
// Wires graceful shutdown on SIGTERM/SIGINT: drain (so /health flips
// to 503 and writes return ErrDraining), then Stop. Configurable via
// environment variables in the 12-factor style for k8s / docker
// compatibility.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/goccy/go-json"
	fiber "github.com/gofiber/fiber/v3"
	"github.com/hyp3rd/ewrap"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/internal/sentinel"
	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
	"github.com/hyp3rd/hypercache/pkg/httpauth"
)

// Defaults applied when the corresponding env var is unset. Centralized
// here so operators see one canonical reference and so the magic-number
// linter doesn't flag repeated literals at the env-parse sites.
const (
	defaultReplication  = 3
	defaultCapacity     = 100_000
	defaultVirtualNodes = 64
	defaultIndirectK    = 2
	suspectMultiplier   = 3 // suspect after = N × heartbeat interval
	deadMultiplier      = 6 // dead    after = N × heartbeat interval
	defaultHintTTL      = 30 * time.Second
	defaultHintReplay   = 200 * time.Millisecond
	defaultHeartbeat    = 1 * time.Second
	defaultRebalance    = 250 * time.Millisecond
	// Membership gossip cadence. Without an enabled gossip loop the
	// cluster has no path to re-introduce a previously-removed node:
	// peers' heartbeats only probe nodes already in their membership
	// list, and the Health endpoint is one-way. A graceful drain →
	// restart (the canonical operator workflow) leaves the restarted
	// node invisible to the rest of the cluster forever. Default 1s
	// matches the heartbeat cadence — gossip+heartbeat together
	// disseminate membership changes within a couple of ticks.
	defaultGossip         = 1 * time.Second
	clientAPIReadTimeout  = 5 * time.Second
	clientAPIWriteTimeout = 5 * time.Second
	clientAPIIdleTimeout  = 60 * time.Second
	shutdownDeadline      = 30 * time.Second
)

// envConfig is the parsed runtime configuration. Defaults reflect a
// reasonable single-node demo posture; production deployments override
// every field via environment variables.
type envConfig struct {
	NodeID       string
	APIAddr      string
	MgmtAddr     string
	DistAddr     string
	Seeds        []string
	Replication  int
	Capacity     int
	AuthPolicy   httpauth.Policy
	APITLSCert   string
	APITLSKey    string
	APITLSCA     string
	LogLevel     slog.Level
	HintTTL      time.Duration
	HintReplay   time.Duration
	Heartbeat    time.Duration
	IndirectK    int
	RebalanceInt time.Duration
	GossipInt    time.Duration

	// Phase C OIDC. When OIDCIssuer is non-empty the binary
	// constructs a ServerVerify closure (see oidc.go) and
	// attaches it to AuthPolicy.ServerVerify so JWTs from the
	// configured IdP authenticate alongside the static-bearer
	// path. All four OIDC fields are required when any one is
	// set; loadConfig fails-fast on partial configuration.
	OIDCIssuer        string
	OIDCAudience      string
	OIDCIdentityClaim string // "sub" or "email"; default "sub"
	OIDCScopeClaim    string // "scope" (space-separated string) or a custom array claim; default "scope"
}

// loadConfig pulls every knob from the environment and applies sane
// defaults. The error return covers auth-policy load failures —
// either the operator set HYPERCACHE_AUTH_CONFIG to a missing/
// malformed file or set both HYPERCACHE_AUTH_CONFIG and
// HYPERCACHE_AUTH_TOKEN. The binary exits non-zero rather than
// silently fall through to open mode (fail-closed by design;
// documented in CHANGELOG as a behavioral change vs pre-v2 where
// any token/config error mapped to permissive open mode).
//
// Other knobs use silent fallbacks to defaults — they are tunables,
// not security boundaries.
func loadConfig() (envConfig, error) {
	policy, err := httpauth.LoadFromEnv()
	if err != nil {
		return envConfig{}, fmt.Errorf("load auth policy: %w", err)
	}

	cfg := envConfig{
		NodeID:            envOr("HYPERCACHE_NODE_ID", hostnameOrDefault()),
		APIAddr:           envOr("HYPERCACHE_API_ADDR", ":8080"),
		MgmtAddr:          envOr("HYPERCACHE_MGMT_ADDR", ":8081"),
		DistAddr:          envOr("HYPERCACHE_DIST_ADDR", ":7946"),
		Seeds:             splitCSV(os.Getenv("HYPERCACHE_SEEDS")),
		Replication:       envInt("HYPERCACHE_REPLICATION", defaultReplication),
		Capacity:          envInt("HYPERCACHE_CAPACITY", defaultCapacity),
		AuthPolicy:        policy,
		APITLSCert:        os.Getenv("HYPERCACHE_API_TLS_CERT"),
		APITLSKey:         os.Getenv("HYPERCACHE_API_TLS_KEY"),
		APITLSCA:          os.Getenv("HYPERCACHE_API_TLS_CLIENT_CA"),
		LogLevel:          parseLogLevel(envOr("HYPERCACHE_LOG_LEVEL", "info")),
		HintTTL:           envDuration("HYPERCACHE_HINT_TTL", defaultHintTTL),
		HintReplay:        envDuration("HYPERCACHE_HINT_REPLAY", defaultHintReplay),
		Heartbeat:         envDuration("HYPERCACHE_HEARTBEAT", defaultHeartbeat),
		IndirectK:         envInt("HYPERCACHE_INDIRECT_PROBE_K", defaultIndirectK),
		RebalanceInt:      envDuration("HYPERCACHE_REBALANCE_INTERVAL", defaultRebalance),
		GossipInt:         envDuration("HYPERCACHE_GOSSIP_INTERVAL", defaultGossip),
		OIDCIssuer:        os.Getenv("HYPERCACHE_OIDC_ISSUER"),
		OIDCAudience:      os.Getenv("HYPERCACHE_OIDC_AUDIENCE"),
		OIDCIdentityClaim: envOr("HYPERCACHE_OIDC_IDENTITY_CLAIM", "sub"),
		OIDCScopeClaim:    envOr("HYPERCACHE_OIDC_SCOPE_CLAIM", "scope"),
	}

	// Phase C: partial OIDC config is fail-fast. Either configure
	// nothing (OIDC disabled) or every required field. Identity
	// and scope claims have defaults; issuer + audience are
	// required when OIDC is enabled.
	err = validateOIDCConfig(cfg)
	if err != nil {
		return envConfig{}, err
	}

	return cfg, nil
}

// errOIDCMissingAudience / errOIDCMissingIssuer are the sentinel
// errors loadConfig wraps when the OIDC env vars are partially
// configured. Static sentinels (vs ad-hoc fmt.Errorf) keep err113
// happy and let callers `errors.Is` against the specific
// misconfiguration.
var (
	errOIDCMissingAudience = ewrap.New("HYPERCACHE_OIDC_ISSUER set without HYPERCACHE_OIDC_AUDIENCE")
	errOIDCMissingIssuer   = ewrap.New("HYPERCACHE_OIDC_AUDIENCE set without HYPERCACHE_OIDC_ISSUER")
)

// validateOIDCConfig returns nil when OIDC is either fully
// configured or fully disabled. Returns a wrapped sentinel when
// only one of (issuer, audience) is set — partial config is the
// most common operator misconfiguration and we want it to surface
// at boot rather than as silent 401s on JWT-bearing requests.
func validateOIDCConfig(cfg envConfig) error {
	if cfg.OIDCIssuer != "" && cfg.OIDCAudience == "" {
		return fmt.Errorf("%w: both are required when OIDC is enabled", errOIDCMissingAudience)
	}

	if cfg.OIDCAudience != "" && cfg.OIDCIssuer == "" {
		return fmt.Errorf("%w: both are required when OIDC is enabled", errOIDCMissingIssuer)
	}

	return nil
}

// attachOIDCVerifier builds the OIDC verifier (when configured)
// and attaches it to cfg.AuthPolicy.ServerVerify BEFORE
// buildHyperCache and registerClientRoutes capture the policy by
// value. The discovery RPC against the IdP happens here
// (blocking) so a misconfigured issuer URL surfaces as a
// fail-fast at startup rather than silent 401s on every
// JWT-bearing request later.
//
// Returns false when verifier construction failed (caller should
// exit non-zero); true on success or when OIDC is disabled.
//
// Extracted from run() to keep its function-length under revive's
// 75-line cap; the verifier-construction step is naturally
// self-contained (one input, one output, one side effect on
// cfg.AuthPolicy).
func attachOIDCVerifier(ctx context.Context, cfg *envConfig, logger *slog.Logger) bool {
	if cfg.OIDCIssuer == "" {
		return true
	}

	verifier, err := buildOIDCVerifier(ctx, *cfg)
	if err != nil {
		logger.Error("oidc verifier construction failed", slog.Any("err", err))

		return false
	}

	cfg.AuthPolicy.ServerVerify = verifier

	return true
}

// envOr returns os.Getenv(key) or fallback when unset/empty.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return fallback
}

// envInt parses an int from env, falling back when unset / invalid.
func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}

	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}

	return n
}

// envDuration parses a Go time.Duration from env, falling back when
// unset / invalid.
func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}

	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}

	return d
}

// splitCSV trims spaces and splits a comma-separated string. Empty
// input returns nil so the dist seed list distinguishes "no seeds"
// from "[empty]".
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}

	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))

	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t != "" {
			out = append(out, t)
		}
	}

	return out
}

// parseLogLevel maps a log-level env string to slog.Level. Unknown
// values fall back to Info; the caller can also set an explicit level
// via slog handler options.
func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// hostnameOrDefault picks os.Hostname() or "node" as a last-resort
// node ID. Stable per-container in Docker (container id) and per-pod
// in k8s.
func hostnameOrDefault() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "node"
	}

	return h
}

// buildHyperCache wires DistMemory + management HTTP into a HyperCache
// configured per the env config. The returned cache is started and
// owns the dist + management HTTP listeners; the caller adds the
// client API server separately and is responsible for graceful Stop.
func buildHyperCache(ctx context.Context, cfg envConfig, logger *slog.Logger) (*hypercache.HyperCache[backend.DistMemory], error) {
	hcCfg, err := hypercache.NewConfig[backend.DistMemory](constants.DistMemoryBackend)
	if err != nil {
		return nil, fmt.Errorf("build hypercache config: %w", err)
	}

	hcCfg.DistMemoryOptions = []backend.DistMemoryOption{
		backend.WithDistNode(cfg.NodeID, cfg.DistAddr),
		backend.WithDistSeeds(cfg.Seeds),
		backend.WithDistReplication(cfg.Replication),
		backend.WithDistVirtualNodes(defaultVirtualNodes),
		backend.WithDistReadConsistency(backend.ConsistencyOne),
		backend.WithDistWriteConsistency(backend.ConsistencyQuorum),
		backend.WithDistHeartbeat(cfg.Heartbeat, suspectMultiplier*cfg.Heartbeat, deadMultiplier*cfg.Heartbeat),
		backend.WithDistIndirectProbes(cfg.IndirectK, cfg.Heartbeat/2),
		backend.WithDistGossipInterval(cfg.GossipInt),
		backend.WithDistHintTTL(cfg.HintTTL),
		backend.WithDistHintReplayInterval(cfg.HintReplay),
		backend.WithDistRebalanceInterval(cfg.RebalanceInt),
		backend.WithDistLogger(logger),
	}

	// Dist transport auth is intentionally separate from the
	// client API's multi-token policy: the cluster is one trust
	// domain (every node holds the same peer token), so reading
	// HYPERCACHE_AUTH_TOKEN directly here keeps the dist symmetry
	// invariant when operators set HYPERCACHE_AUTH_CONFIG for the
	// client API but still want peer auth on the wire.
	if peerToken := os.Getenv(httpauth.EnvAuthToken); peerToken != "" {
		hcCfg.DistMemoryOptions = append(
			hcCfg.DistMemoryOptions,
			backend.WithDistHTTPAuth(backend.DistHTTPAuth{Token: peerToken}),
		)
	}

	// Phase C2: light up scope enforcement on the management port.
	// /health stays public (k8s liveness probes carry no creds).
	// Read-or-better is required for the observability surface
	// (/stats, /config, /dist/*, /cluster/*); admin scope is
	// required for the cluster-mutating control routes (/evict,
	// /clear, /trigger-expiration). Closes a long-standing gap
	// where the mgmt port was fully unauthenticated server-side
	// while the monitor's proxy carried the only check.
	//
	// Closure captures cfg.AuthPolicy by value — Policy is value-
	// semantic and safe for concurrent use after construction;
	// see pkg/httpauth/policy.go.
	policy := cfg.AuthPolicy
	mgmtReadAuth := func(fiberCtx fiber.Ctx) error {
		return policy.Verify(fiberCtx, httpauth.ScopeRead)
	}
	mgmtAdminAuth := func(fiberCtx fiber.Ctx) error {
		return policy.Verify(fiberCtx, httpauth.ScopeAdmin)
	}

	hcCfg.HyperCacheOptions = append(
		hcCfg.HyperCacheOptions,
		hypercache.WithManagementHTTP[backend.DistMemory](
			cfg.MgmtAddr,
			hypercache.WithMgmtAuth(mgmtReadAuth),
			hypercache.WithMgmtControlAuth(mgmtAdminAuth),
		),
		// Surfaces eviction/expiration loop start, per-tick activity,
		// and the cluster-join startup summary in the binary's JSON
		// log stream. Without this the HyperCache wrapper runs silent.
		hypercache.WithLogger[backend.DistMemory](logger),
	)

	hc, err := hypercache.New(ctx, hypercache.GetDefaultManager(), hcCfg)
	if err != nil {
		return nil, fmt.Errorf("construct hypercache: %w", err)
	}

	return hc, nil
}

// nodeContext bundles the per-server values handlers need so they can
// surface routing information (this node's ID, the ring's owners for
// a key) in their responses without re-deriving from the raw fiber
// context every call.
type nodeContext struct {
	hc     *hypercache.HyperCache[backend.DistMemory]
	nodeID string
}

// errorResponse is the canonical JSON error shape for the client API.
// Every 4xx / 5xx response carries this payload — operators can grep
// `code` to classify failures without parsing free-text messages.
type errorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// API error codes — kept as string constants for stable identity in
// machine-readable consumers (alerting rules, client SDKs).
const (
	codeBadRequest = "BAD_REQUEST"
	codeNotFound   = "NOT_FOUND"
	codeDraining   = "DRAINING"
	codeInternal   = "INTERNAL"
)

// TLS-config sentinel errors returned by buildAPITLSConfig. Wrapped
// via fmt.Errorf at construction time so callers see the field
// name + path; matched via errors.Is for control-flow.
var (
	errAPITLSPartial   = ewrap.New("HYPERCACHE_API_TLS_CERT and HYPERCACHE_API_TLS_KEY must both be set")
	errAPITLSNoPEMInCA = ewrap.New("HYPERCACHE_API_TLS_CLIENT_CA: no PEM certificates parsed from file")
)

// registerClientRoutes wires every client-API route onto the
// provided fiber app. Extracted from runClientAPI so tests
// (handlers_test.go, auth_test.go, openapi_test.go) drive the same
// wiring without spinning up a real listener — and so the drift
// test can introspect routes from the *exact* production
// registration rather than a hand-maintained mirror.
//
// Routes are scope-tagged: read endpoints (GET/HEAD/owners-lookup,
// batch-get) require ScopeRead; mutating endpoints (PUT/DELETE,
// batch-put/delete) require ScopeWrite. /healthz and
// /v1/openapi.yaml are deliberately scope-less so liveness probes
// and spec-discovery work without credentials.
//
// When the policy is unconfigured (zero Policy with AllowAnonymous
// false), every protected route 401s — fail-closed by design. The
// hypercache-server binary's loadConfig handles the legacy
// "neither env var set" path by flipping AllowAnonymous on with a
// startup warning, so the zero-config dev posture still works.
func registerClientRoutes(app *fiber.App, policy httpauth.Policy, nodeCtx *nodeContext) {
	read := policy.Middleware(httpauth.ScopeRead)
	write := policy.Middleware(httpauth.ScopeWrite)

	app.Get("/healthz", func(c fiber.Ctx) error { return c.SendString("ok") })

	// Self-describing — clients can discover the API surface
	// without out-of-band docs. The spec is embedded at build
	// time from cmd/hypercache-server/openapi.yaml so it stays
	// in lockstep with whatever the binary was built against.
	app.Get("/v1/openapi.yaml", func(c fiber.Ctx) error {
		c.Set(fiber.HeaderContentType, "application/yaml")

		return c.Send(openapiSpec)
	})

	// /v1/cache/keys must be registered BEFORE the parameterized
	// /v1/cache/:key — Fiber matches in registration order and the
	// literal-path route would otherwise be shadowed by the
	// param-bound handler (handleGet would be invoked with
	// `key="keys"` and return 404).
	app.Get("/v1/cache/keys", read, func(c fiber.Ctx) error { return handleListKeys(c, nodeCtx) })
	app.Put("/v1/cache/:key", write, func(c fiber.Ctx) error { return handlePut(c, nodeCtx) })
	app.Get("/v1/cache/:key", read, func(c fiber.Ctx) error { return handleGet(c, nodeCtx) })
	app.Head("/v1/cache/:key", read, func(c fiber.Ctx) error { return handleHead(c, nodeCtx) })
	app.Delete("/v1/cache/:key", write, func(c fiber.Ctx) error { return handleDelete(c, nodeCtx) })
	app.Get("/v1/owners/:key", read, func(c fiber.Ctx) error { return handleOwners(c, nodeCtx) })
	app.Get("/v1/me", read, handleMe)
	app.Get("/v1/me/can", read, handleCan)

	app.Post("/v1/cache/batch/get", read, func(c fiber.Ctx) error { return handleBatchGet(c, nodeCtx) })
	app.Post("/v1/cache/batch/put", write, func(c fiber.Ctx) error { return handleBatchPut(c, nodeCtx) })
	app.Post("/v1/cache/batch/delete", write, func(c fiber.Ctx) error { return handleBatchDelete(c, nodeCtx) })
}

// runClientAPI builds and starts the client REST API. Returns the
// fiber app so main can shut it down on signal. The provided
// httpauth.Policy gates every protected route — see
// registerClientRoutes for the per-route scope mapping.
//
// TLS posture (controlled via cfg, mirroring dist's pattern):
//
//   - cfg.APITLSCert + cfg.APITLSKey both set → standard TLS.
//   - Adding cfg.APITLSCA → mTLS with RequireAndVerifyClientCert.
//     The verified peer cert's Subject CN is what
//     httpauth.Policy.CertIdentities matches against.
//   - Either field empty → plaintext on cfg.APIAddr (preserves
//     today's default behavior; dev mode and ingress-terminated TLS
//     setups keep working).
//
// Listener-construction errors fail fast (the goroutine wouldn't
// have surfaced them anyway via app.Listener); operators see the
// failure at startup rather than silently bound on the wrong
// protocol.
func runClientAPI(
	cfg envConfig,
	hc *hypercache.HyperCache[backend.DistMemory],
	logger *slog.Logger,
) (*fiber.App, error) {
	app := fiber.New(fiber.Config{
		AppName:      "hypercache-server",
		ReadTimeout:  clientAPIReadTimeout,
		WriteTimeout: clientAPIWriteTimeout,
		IdleTimeout:  clientAPIIdleTimeout,
	})

	registerClientRoutes(app, cfg.AuthPolicy, &nodeContext{hc: hc, nodeID: cfg.NodeID})

	tlsCfg, err := buildAPITLSConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("build client API TLS config: %w", err)
	}

	if tlsCfg == nil {
		go runPlaintextListener(app, cfg.APIAddr, logger)

		return app, nil
	}

	ln, err := tls.Listen("tcp", cfg.APIAddr, tlsCfg)
	if err != nil {
		return nil, fmt.Errorf("client API tls listen %s: %w", cfg.APIAddr, err)
	}

	go runWrappedListener(app, ln, logger)

	return app, nil
}

// runPlaintextListener serves on the bare addr — the standard
// non-TLS path that shipped pre-v2.
func runPlaintextListener(app *fiber.App, addr string, logger *slog.Logger) {
	err := app.Listen(addr)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("client API listener exited", slog.Any("err", err))
	}
}

// runWrappedListener serves on a pre-built net.Listener (used by
// the TLS path so the tls.Config — including ClientAuth and
// ClientCAs — is fully under our control).
func runWrappedListener(app *fiber.App, ln net.Listener, logger *slog.Logger) {
	err := app.Listener(ln)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("client API listener exited", slog.Any("err", err))
	}
}

// buildAPITLSConfig assembles the *tls.Config the API listener
// should use, or nil for the plaintext default. CERT+KEY are the
// minimum for TLS; adding CA upgrades to mTLS with
// RequireAndVerifyClientCert (the only mode that gives the auth
// middleware a verified peer cert to map to a CertIdentity).
//
// Returns an error when CERT or KEY is set but the other is missing
// — that shape is operator-misconfiguration, not "TLS off." Don't
// silently fall through to plaintext when the operator clearly
// asked for TLS but typo'd one of the paths.
func buildAPITLSConfig(cfg envConfig) (*tls.Config, error) {
	if cfg.APITLSCert == "" && cfg.APITLSKey == "" {
		return nil, nil //nolint:nilnil // documented "no TLS" sentinel
	}

	if cfg.APITLSCert == "" || cfg.APITLSKey == "" {
		return nil, errAPITLSPartial
	}

	cert, err := tls.LoadX509KeyPair(cfg.APITLSCert, cfg.APITLSKey)
	if err != nil {
		return nil, fmt.Errorf("load TLS keypair: %w", err)
	}

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}

	if cfg.APITLSCA == "" {
		return tlsCfg, nil
	}

	caPEM, err := os.ReadFile(cfg.APITLSCA)
	if err != nil {
		return nil, fmt.Errorf("read client CA bundle %s: %w", cfg.APITLSCA, err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("%w: %s", errAPITLSNoPEMInCA, cfg.APITLSCA)
	}

	tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
	tlsCfg.ClientCAs = pool

	return tlsCfg, nil
}

// jsonErr writes the canonical errorResponse with the given status
// + code + message. Centralized so every error path emits the same
// shape regardless of which handler is failing.
func jsonErr(c fiber.Ctx, status int, code, msg string) error {
	return c.Status(status).JSON(errorResponse{Error: msg, Code: code})
}

// classifyAndRespond maps a service-level error to the right HTTP
// status + code. Keeps the per-handler error-handling tight and
// guarantees that adding a new sentinel anywhere in the stack only
// needs one update site.
func classifyAndRespond(c fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, sentinel.ErrDraining):
		return jsonErr(c, fiber.StatusServiceUnavailable, codeDraining, "node is draining; redirect to a peer")
	case errors.Is(err, sentinel.ErrNotOwner):
		return jsonErr(c, fiber.StatusServiceUnavailable, codeInternal, "no ring owners for key (cluster initializing?)")
	default:
		return jsonErr(c, fiber.StatusInternalServerError, codeInternal, err.Error())
	}
}

// putResponse documents the JSON shape returned on a successful PUT.
// Owners + Node let the operator immediately see where the value
// landed in the ring — invaluable when debugging cluster topology
// without having to chase /dist/owners on the management HTTP.
type putResponse struct {
	Key    string   `json:"key"`
	Stored bool     `json:"stored"`
	TTLMs  int64    `json:"ttl_ms,omitempty"`
	Bytes  int      `json:"bytes"`
	Node   string   `json:"node"`
	Owners []string `json:"owners"`
}

// deleteResponse mirrors putResponse for DELETE — owners are useful
// because the deletion fans out to every replica in the ring.
type deleteResponse struct {
	Key     string   `json:"key"`
	Deleted bool     `json:"deleted"`
	Node    string   `json:"node"`
	Owners  []string `json:"owners"`
}

// ownersResponse is the body of GET /v1/owners/:key — pure visibility
// endpoint that mirrors what the dist HTTP server reports to peers.
type ownersResponse struct {
	Key    string   `json:"key"`
	Owners []string `json:"owners"`
	Node   string   `json:"node"`
}

// listKeysResponse is the body of GET /v1/cache/keys — operator-
// facing key browser. `NextCursor` is empty on the last page;
// `TotalMatched` is the full deduplicated matched set (capped by
// `max`). `Truncated` reports that the cluster-wide cap was hit
// and the operator should refine the pattern. `PartialNodes`
// lists peers whose fan-out failed; their keys may be missing.
type listKeysResponse struct {
	Keys         []string `json:"keys"`
	NextCursor   string   `json:"next_cursor"`
	TotalMatched int      `json:"total_matched"`
	Truncated    bool     `json:"truncated"`
	Node         string   `json:"node"`
	PartialNodes []string `json:"partial_nodes,omitempty"`
}

// list-keys query-parameter bounds. Defaults match the operator
// "browse / refine" workflow; the hard caps bound the worst-case
// memory and response size — operators needing a larger sweep
// script against the per-node /internal/keys path with their own
// paging instead of lifting these.
const (
	listKeysDefaultLimit = 100
	listKeysMaxLimit     = 500
	listKeysDefaultMax   = 10000
	listKeysHardMax      = 50000
)

// handlePut implements PUT /v1/cache/:key.
// Body is the raw value (any content type). Optional ?ttl=<dur>
// applies a relative expiration; empty/absent means no expiration.
// Returns 200 with a putResponse body summarizing key, ttl, bytes
// stored, the writing node's ID, and the ring owners — the
// owners list is the operator's visibility into where the value
// actually landed across the cluster.
func handlePut(c fiber.Ctx, nodeCtx *nodeContext) error {
	key := c.Params("key")
	if key == "" {
		return jsonErr(c, fiber.StatusBadRequest, codeBadRequest, "missing key in path")
	}

	ttl := time.Duration(0)

	if raw := c.Query("ttl"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return jsonErr(c, fiber.StatusBadRequest, codeBadRequest, "invalid ttl: "+err.Error())
		}

		ttl = parsed
	}

	body := c.Body()

	value := make([]byte, len(body))
	copy(value, body) // detach from fiber's pooled body buffer

	err := nodeCtx.hc.Set(c.Context(), key, value, ttl)
	if err != nil {
		return classifyAndRespond(c, err)
	}

	return c.JSON(putResponse{
		Key:    key,
		Stored: true,
		TTLMs:  ttl.Milliseconds(),
		Bytes:  len(value),
		Node:   nodeCtx.nodeID,
		Owners: nodeCtx.hc.ClusterOwners(key),
	})
}

// itemEnvelope is the JSON shape returned when the client asks for
// `Accept: application/json` on a single-key GET. Values are always
// emitted as base64 in the envelope so the response is binary-safe
// without the heuristic decode dance the raw-bytes path uses —
// callers that want the literal string can decode the base64
// themselves.
type itemEnvelope struct {
	Key           string   `json:"key"`
	Value         string   `json:"value"`
	ValueEncoding string   `json:"value_encoding"`
	TTLMs         int64    `json:"ttl_ms,omitempty"`
	ExpiresAt     string   `json:"expires_at,omitempty"`
	Version       uint64   `json:"version"`
	Origin        string   `json:"origin,omitempty"`
	LastUpdated   string   `json:"last_updated,omitempty"`
	Node          string   `json:"node"`
	Owners        []string `json:"owners"`
}

// wantsJSON reports whether the client explicitly asked for the JSON
// envelope via Accept. A bare `*/*` or absent header keeps the
// raw-bytes default — operators using `curl -X GET` with no Accept
// header continue to see the literal value, not a base64 envelope.
func wantsJSON(c fiber.Ctx) bool {
	accept := c.Get(fiber.HeaderAccept)
	if accept == "" {
		return false
	}

	return strings.Contains(accept, fiber.MIMEApplicationJSON)
}

// itemValueAsBytes normalizes the cached value to its underlying
// byte representation regardless of how it round-tripped through
// the dist HTTP transport (writer-node []byte vs replica-node
// base64-string vs non-owner json.RawMessage). Reuses the same
// heuristics as writeValue so single-key and batch responses stay
// in agreement.
func itemValueAsBytes(v any) []byte {
	switch x := v.(type) {
	case []byte:
		return x

	case string:
		if decoded, ok := decodeBase64Bytes(x); ok {
			return decoded
		}

		return []byte(x)

	case json.RawMessage:
		var s string

		err := json.Unmarshal(x, &s)
		if err == nil {
			if decoded, ok := decodeBase64Bytes(s); ok {
				return decoded
			}

			return []byte(s)
		}

		return []byte(x)

	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return nil
		}

		return raw
	}
}

// itemRemainingTTL returns (ttl_ms, expires_at_iso) for an Item.
// Returns (0, "") when the item has no expiration. Negative
// remaining TTLs are clamped to 0 — a "currently expiring" item
// is reported as 0ms left, not as a negative number.
func itemRemainingTTL(it *cache.Item) (int64, string) {
	if it.Expiration <= 0 {
		return 0, ""
	}

	expiry := it.LastAccess.Add(it.Expiration)
	remaining := max(time.Until(expiry).Milliseconds(), 0)

	return remaining, expiry.UTC().Format(time.RFC3339)
}

// buildEnvelope constructs the JSON envelope for a cached item.
// Centralized so the single-key GET and the batch-get response
// emit identical shapes.
func buildEnvelope(key string, it *cache.Item, nodeCtx *nodeContext) itemEnvelope {
	bytes := itemValueAsBytes(it.Value)
	ttlMs, expiresAt := itemRemainingTTL(it)

	env := itemEnvelope{
		Key:           key,
		Value:         base64.StdEncoding.EncodeToString(bytes),
		ValueEncoding: "base64",
		TTLMs:         ttlMs,
		ExpiresAt:     expiresAt,
		Version:       it.Version,
		Origin:        it.Origin,
		Node:          nodeCtx.nodeID,
		Owners:        nodeCtx.hc.ClusterOwners(key),
	}

	if !it.LastUpdated.IsZero() {
		env.LastUpdated = it.LastUpdated.UTC().Format(time.RFC3339)
	}

	return env
}

// setItemHeaders mirrors buildEnvelope onto response headers — the
// HEAD handler returns these without a body. Header names use the
// `X-Cache-*` convention; values are best-effort string forms.
func setItemHeaders(c fiber.Ctx, key string, it *cache.Item, nodeCtx *nodeContext) {
	c.Set("X-Cache-Version", strconv.FormatUint(it.Version, 10))

	if it.Origin != "" {
		c.Set("X-Cache-Origin", it.Origin)
	}

	if !it.LastUpdated.IsZero() {
		c.Set("X-Cache-Last-Updated", it.LastUpdated.UTC().Format(time.RFC3339))
	}

	ttlMs, expiresAt := itemRemainingTTL(it)
	if ttlMs > 0 {
		c.Set("X-Cache-TTL-Ms", strconv.FormatInt(ttlMs, 10))
		c.Set("X-Cache-Expires-At", expiresAt)
	}

	owners := nodeCtx.hc.ClusterOwners(key)
	if len(owners) > 0 {
		c.Set("X-Cache-Owners", strings.Join(owners, ","))
	}

	c.Set("X-Cache-Node", nodeCtx.nodeID)
}

// handleGet implements GET /v1/cache/:key.
//
// Default response: raw bytes with Content-Type application/octet-stream
// (binary fidelity, current behavior).
//
// Accept: application/json: itemEnvelope JSON with TTL, version,
// owners, etc. Lets API clients fetch metadata in one round-trip
// instead of GET + HEAD.
func handleGet(c fiber.Ctx, nodeCtx *nodeContext) error {
	key := c.Params("key")
	if key == "" {
		return jsonErr(c, fiber.StatusBadRequest, codeBadRequest, "missing key in path")
	}

	it, ok := nodeCtx.hc.GetWithInfo(c.Context(), key)
	if !ok {
		return jsonErr(c, fiber.StatusNotFound, codeNotFound, "key not found")
	}

	if wantsJSON(c) {
		return c.JSON(buildEnvelope(key, it, nodeCtx))
	}

	return writeValue(c, it.Value)
}

// batchGetRequest documents the request shape for
// `POST /v1/cache/batch/get`. Empty `keys` returns an empty
// `results` array with status 200.
type batchGetRequest struct {
	Keys []string `json:"keys"`
}

// batchGetResult is one entry in the batch-get response. `Found:
// false` results carry no metadata; `Found: true` results carry
// the same envelope shape as a single-key Accept:json GET.
type batchGetResult struct {
	Key           string   `json:"key"`
	Found         bool     `json:"found"`
	Value         string   `json:"value,omitempty"`
	ValueEncoding string   `json:"value_encoding,omitempty"`
	TTLMs         int64    `json:"ttl_ms,omitempty"`
	ExpiresAt     string   `json:"expires_at,omitempty"`
	Version       uint64   `json:"version,omitempty"`
	Origin        string   `json:"origin,omitempty"`
	LastUpdated   string   `json:"last_updated,omitempty"`
	Owners        []string `json:"owners,omitempty"`
}

// batchGetResponse is the top-level wrapper so a future caller can
// add cluster-wide stats (per-batch latency, owners-touched, etc.)
// without breaking the wire shape.
type batchGetResponse struct {
	Results []batchGetResult `json:"results"`
	Node    string           `json:"node"`
}

// batchPutItem is one entry in the batch-put request. `value` is
// either a UTF-8 string (default) or a base64-encoded byte payload
// when `value_encoding` is `"base64"` — the same convention the
// single-key Accept:json GET emits, so a batch-put can round-trip
// the result of an earlier batch-get verbatim.
type batchPutItem struct {
	Key           string `json:"key"`
	Value         string `json:"value"`
	ValueEncoding string `json:"value_encoding,omitempty"`
	TTLMs         int64  `json:"ttl_ms,omitempty"`
}

type batchPutRequest struct {
	Items []batchPutItem `json:"items"`
}

// batchPutResult is one entry in the batch-put response. On
// failure, `Stored` is false and `Error`/`Code` describe why —
// per-item granularity so a single failing item doesn't void
// the whole batch.
type batchPutResult struct {
	Key    string   `json:"key"`
	Stored bool     `json:"stored"`
	Bytes  int      `json:"bytes,omitempty"`
	Owners []string `json:"owners,omitempty"`
	Error  string   `json:"error,omitempty"`
	Code   string   `json:"code,omitempty"`
}

type batchPutResponse struct {
	Results []batchPutResult `json:"results"`
	Node    string           `json:"node"`
}

// batchDeleteResult is one entry in the batch-delete response.
type batchDeleteResult struct {
	Key     string   `json:"key"`
	Deleted bool     `json:"deleted"`
	Owners  []string `json:"owners,omitempty"`
	Error   string   `json:"error,omitempty"`
	Code    string   `json:"code,omitempty"`
}

type batchDeleteRequest struct {
	Keys []string `json:"keys"`
}

type batchDeleteResponse struct {
	Results []batchDeleteResult `json:"results"`
	Node    string              `json:"node"`
}

// handleBatchGet implements POST /v1/cache/batch/get — fetches
// many keys in one round-trip with the same metadata envelope as
// the single-key Accept:json GET. Each key's lookup is
// independent: a missing key produces `{found: false}` rather
// than failing the whole batch.
func handleBatchGet(c fiber.Ctx, nodeCtx *nodeContext) error {
	var req batchGetRequest

	err := json.Unmarshal(c.Body(), &req)
	if err != nil {
		return jsonErr(c, fiber.StatusBadRequest, codeBadRequest, "invalid JSON: "+err.Error())
	}

	results := make([]batchGetResult, 0, len(req.Keys))
	ctx := c.Context()

	for _, key := range req.Keys {
		if key == "" {
			results = append(results, batchGetResult{Key: key, Found: false})

			continue
		}

		it, ok := nodeCtx.hc.GetWithInfo(ctx, key)
		if !ok {
			results = append(results, batchGetResult{Key: key, Found: false})

			continue
		}

		results = append(results, batchGetResultFromItem(key, it, nodeCtx))
	}

	return c.JSON(batchGetResponse{Results: results, Node: nodeCtx.nodeID})
}

// batchGetResultFromItem mirrors buildEnvelope's projection —
// shared with the single-key Accept:json GET path so the wire
// shape stays consistent.
func batchGetResultFromItem(key string, it *cache.Item, nodeCtx *nodeContext) batchGetResult {
	bytes := itemValueAsBytes(it.Value)
	ttlMs, expiresAt := itemRemainingTTL(it)

	res := batchGetResult{
		Key:           key,
		Found:         true,
		Value:         base64.StdEncoding.EncodeToString(bytes),
		ValueEncoding: "base64",
		TTLMs:         ttlMs,
		ExpiresAt:     expiresAt,
		Version:       it.Version,
		Origin:        it.Origin,
		Owners:        nodeCtx.hc.ClusterOwners(key),
	}

	if !it.LastUpdated.IsZero() {
		res.LastUpdated = it.LastUpdated.UTC().Format(time.RFC3339)
	}

	return res
}

// handleBatchPut implements POST /v1/cache/batch/put. Each item's
// `value_encoding` selects how the wire `value` string is
// interpreted: `"base64"` decodes bytes-first; anything else
// (including absent) treats the string as UTF-8 text and stores
// the raw bytes. Per-item errors are carried in the response —
// a single failure doesn't void the whole batch.
func handleBatchPut(c fiber.Ctx, nodeCtx *nodeContext) error {
	var req batchPutRequest

	err := json.Unmarshal(c.Body(), &req)
	if err != nil {
		return jsonErr(c, fiber.StatusBadRequest, codeBadRequest, "invalid JSON: "+err.Error())
	}

	results := make([]batchPutResult, 0, len(req.Items))
	ctx := c.Context()

	for _, item := range req.Items {
		results = append(results, applyBatchPutItem(ctx, nodeCtx, item))
	}

	return c.JSON(batchPutResponse{Results: results, Node: nodeCtx.nodeID})
}

// applyBatchPutItem decodes a single batch-put item and forwards
// it to the cache. Extracted so handleBatchPut stays readable
// despite the value-encoding branch.
func applyBatchPutItem(ctx context.Context, nodeCtx *nodeContext, item batchPutItem) batchPutResult {
	if item.Key == "" {
		return batchPutResult{Key: item.Key, Stored: false, Error: "missing key", Code: codeBadRequest}
	}

	value, decodeErr := decodeBatchPutValue(item)
	if decodeErr != nil {
		return batchPutResult{Key: item.Key, Stored: false, Error: decodeErr.Error(), Code: codeBadRequest}
	}

	ttl := time.Duration(item.TTLMs) * time.Millisecond

	setErr := nodeCtx.hc.Set(ctx, item.Key, value, ttl)
	if setErr != nil {
		return batchPutResult{
			Key:    item.Key,
			Stored: false,
			Error:  setErr.Error(),
			Code:   classifyErrCode(setErr),
		}
	}

	return batchPutResult{
		Key:    item.Key,
		Stored: true,
		Bytes:  len(value),
		Owners: nodeCtx.hc.ClusterOwners(item.Key),
	}
}

// decodeBatchPutValue interprets the wire `value` string per its
// `value_encoding`. Absent / unknown encoding is treated as
// "string" (UTF-8 text bytes).
func decodeBatchPutValue(item batchPutItem) ([]byte, error) {
	if item.ValueEncoding != "base64" {
		return []byte(item.Value), nil
	}

	decoded, err := base64.StdEncoding.DecodeString(item.Value)
	if err != nil {
		return nil, fmt.Errorf("invalid base64 value: %w", err)
	}

	return decoded, nil
}

// handleBatchDelete implements POST /v1/cache/batch/delete. Same
// per-item granularity as handleBatchPut.
func handleBatchDelete(c fiber.Ctx, nodeCtx *nodeContext) error {
	var req batchDeleteRequest

	err := json.Unmarshal(c.Body(), &req)
	if err != nil {
		return jsonErr(c, fiber.StatusBadRequest, codeBadRequest, "invalid JSON: "+err.Error())
	}

	results := make([]batchDeleteResult, 0, len(req.Keys))
	ctx := c.Context()

	for _, key := range req.Keys {
		if key == "" {
			results = append(results, batchDeleteResult{Key: key, Deleted: false, Error: "missing key", Code: codeBadRequest})

			continue
		}

		owners := nodeCtx.hc.ClusterOwners(key)

		removeErr := nodeCtx.hc.Remove(ctx, key)
		if removeErr != nil {
			results = append(results, batchDeleteResult{
				Key:    key,
				Owners: owners,
				Error:  removeErr.Error(),
				Code:   classifyErrCode(removeErr),
			})

			continue
		}

		results = append(results, batchDeleteResult{Key: key, Deleted: true, Owners: owners})
	}

	return c.JSON(batchDeleteResponse{Results: results, Node: nodeCtx.nodeID})
}

// classifyErrCode maps a service-level error to the canonical
// machine-readable code string. Mirrors classifyAndRespond's
// status mapping but returns just the code so per-item batch
// results can include it without overriding the batch's HTTP
// status.
func classifyErrCode(err error) string {
	switch {
	case errors.Is(err, sentinel.ErrDraining):
		return codeDraining
	case errors.Is(err, sentinel.ErrNotOwner):
		return codeInternal
	default:
		return codeInternal
	}
}

// handleHead implements HEAD /v1/cache/:key — fast metadata
// inspection. Returns 200 with X-Cache-* response headers when
// the key is present, 404 when absent. No body.
//
// Lets clients check existence + remaining TTL + version
// without paying the value-transfer cost. Useful for
// cache-revalidation flows and conditional logic.
func handleHead(c fiber.Ctx, nodeCtx *nodeContext) error {
	key := c.Params("key")
	if key == "" {
		return c.SendStatus(fiber.StatusBadRequest)
	}

	it, ok := nodeCtx.hc.GetWithInfo(c.Context(), key)
	if !ok {
		return c.SendStatus(fiber.StatusNotFound)
	}

	setItemHeaders(c, key, it, nodeCtx)

	return c.SendStatus(fiber.StatusOK)
}

// writeValue emits a cached value back to the client with the right
// Content-Type. The wire format used by the dist HTTP transport
// JSON-marshals Item.Value (typed `any`); on the receiving node a
// `[]byte` written by an upstream PUT becomes a base64-encoded
// `string` after the JSON round-trip. Without compensation the GET
// on a non-writer replica returns `d29ybGQ=` instead of `world`,
// which is the asymmetric behavior the user (rightly) flagged.
//
// The compensation: when the in-memory value is a `string` that is
// valid standard-base64 of plausible byte length, decode it and
// emit the underlying bytes. Falls back to the literal string when
// the decode fails or the result is empty — strings that *aren't*
// base64-encoded bytes (set via PUT with text/* body that happened
// to be stored as-is) keep round-tripping cleanly.
func writeValue(c fiber.Ctx, v any) error {
	c.Set(fiber.HeaderContentType, "application/octet-stream")

	switch x := v.(type) {
	case []byte:
		return c.Send(x)

	case string:
		if decoded, ok := decodeBase64Bytes(x); ok {
			return c.Send(decoded)
		}

		return c.SendString(x)

	case json.RawMessage:
		return writeRawJSON(c, x)

	default:
		c.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)

		return c.JSON(v)
	}
}

// writeRawJSON renders a `json.RawMessage` value back to the client.
// The dist HTTP transport's ForwardGet decodes Item.Value as a
// `json.RawMessage` to preserve the wire-bytes' type fidelity — so
// when this node forwards a Get to the owning peer, the value comes
// back as raw JSON (e.g. `"d29ybGQ="` *with the surrounding quotes*).
//
// We try to interpret the raw JSON as a string first; that's the
// shape of every value originally written through the client API
// (PUT body → []byte → JSON-marshaled as base64 → unquoted string
// when peers receive it). If the string is base64, decode and emit
// the bytes; otherwise emit the unquoted string. When the JSON
// isn't a string at all (numbers, objects, arrays), fall back to
// emitting the raw bytes with `application/json` so structured
// values still round-trip cleanly.
func writeRawJSON(c fiber.Ctx, raw json.RawMessage) error {
	var s string

	err := json.Unmarshal(raw, &s)
	if err == nil {
		if decoded, ok := decodeBase64Bytes(s); ok {
			return c.Send(decoded)
		}

		return c.SendString(s)
	}

	c.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)

	return c.Send(raw)
}

// decodeBase64Bytes returns (decoded, true) when s is a non-empty
// valid standard-base64 encoding of byte content; otherwise (nil,
// false). The minimum-length check (>=4) avoids treating 1–3 byte
// strings (which can never be valid standard-base64 padded output)
// as base64 candidates.
func decodeBase64Bytes(s string) ([]byte, bool) {
	const minB64 = 4

	if len(s) < minB64 || len(s)%minB64 != 0 {
		return nil, false
	}

	out, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(out) == 0 {
		return nil, false
	}

	return out, true
}

// handleDelete implements DELETE /v1/cache/:key.
// Returns 200 with a deleteResponse body. The owners list is
// captured BEFORE the Remove call so a draining-or-otherwise-
// failing delete still tells the operator where the key was
// supposed to live — useful for follow-up retries against a
// peer.
func handleDelete(c fiber.Ctx, nodeCtx *nodeContext) error {
	key := c.Params("key")
	if key == "" {
		return jsonErr(c, fiber.StatusBadRequest, codeBadRequest, "missing key in path")
	}

	owners := nodeCtx.hc.ClusterOwners(key)

	err := nodeCtx.hc.Remove(c.Context(), key)
	if err != nil {
		return classifyAndRespond(c, err)
	}

	return c.JSON(deleteResponse{
		Key:     key,
		Deleted: true,
		Node:    nodeCtx.nodeID,
		Owners:  owners,
	})
}

// handleOwners implements GET /v1/owners/:key — operator visibility
// into the ring without needing the management HTTP port. Returns
// the owners array even when the key has never been written, since
// the ring is deterministic from the key + membership.
func handleOwners(c fiber.Ctx, nodeCtx *nodeContext) error {
	key := c.Params("key")
	if key == "" {
		return jsonErr(c, fiber.StatusBadRequest, codeBadRequest, "missing key in path")
	}

	return c.JSON(ownersResponse{
		Key:    key,
		Owners: nodeCtx.hc.ClusterOwners(key),
		Node:   nodeCtx.nodeID,
	})
}

// listKeysParams is the parsed-and-validated form of the
// /v1/cache/keys query string. Returned as a struct so
// parseListKeysQuery stays under the function-result-limit and
// the call site reads fields by name rather than position.
type listKeysParams struct {
	Pattern    string
	Cursor     int
	Limit      int
	MaxResults int
}

// parseBoundedPositiveInt reads a query parameter as a positive int
// with a default fallback and a hard ceiling. Empty value → default.
// Out-of-range or non-numeric → caller-visible error (must surface
// as 400 BAD_REQUEST).
func parseBoundedPositiveInt(c fiber.Ctx, name string, def, hardMax int) (int, error) {
	v := c.Query(name)
	if v == "" {
		return def, nil
	}

	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, ewrap.New("invalid " + name + ": must be a positive integer")
	}

	if n > hardMax {
		n = hardMax
	}

	return n, nil
}

// parseListKeysQuery extracts and validates the query parameters
// for GET /v1/cache/keys. Defaults and hard caps are applied here
// so handleListKeys keeps a single response-shape concern.
func parseListKeysQuery(c fiber.Ctx) (listKeysParams, error) {
	out := listKeysParams{Pattern: c.Query("q")}

	if cursorStr := c.Query("cursor"); cursorStr != "" {
		n, err := strconv.Atoi(cursorStr)
		if err != nil || n < 0 {
			return listKeysParams{}, ewrap.New("invalid cursor: must be a non-negative integer")
		}

		out.Cursor = n
	}

	limit, err := parseBoundedPositiveInt(c, "limit", listKeysDefaultLimit, listKeysMaxLimit)
	if err != nil {
		return listKeysParams{}, err
	}

	out.Limit = limit

	maxResults, err := parseBoundedPositiveInt(c, "max", listKeysDefaultMax, listKeysHardMax)
	if err != nil {
		return listKeysParams{}, err
	}

	out.MaxResults = maxResults

	return out, nil
}

// handleListKeys implements GET /v1/cache/keys — operator-facing
// cluster-wide key browser. Fans out across every alive peer,
// merges + dedupes + sorts the result, then slices the page via
// cursor/limit. The full deduplicated set is held in memory for
// one request (bounded by `max`); paging re-fans out — fine for
// the operator-debug workflow this endpoint serves.
//
// Returns 501 when the underlying backend isn't a DistMemory
// (in-memory / Redis): the surface only makes sense in cluster
// mode and surfacing that explicitly is friendlier than a
// silently empty page.
func handleListKeys(c fiber.Ctx, nodeCtx *nodeContext) error {
	params, err := parseListKeysQuery(c)
	if err != nil {
		return jsonErr(c, fiber.StatusBadRequest, codeBadRequest, err.Error())
	}

	res, err := nodeCtx.hc.ClusterKeys(c.Context(), params.Pattern, params.MaxResults)
	if err != nil {
		return jsonErr(c, fiber.StatusBadRequest, codeBadRequest, err.Error())
	}

	if res == nil {
		return jsonErr(
			c,
			fiber.StatusNotImplemented,
			codeInternal,
			"list-keys requires a distributed backend",
		)
	}

	total := len(res.Keys)

	// Cursor past the end is a valid terminal state (last page +
	// 1): respond with an empty page rather than 400. Mirrors how
	// SQL OFFSET past the row count returns an empty result set.
	start := min(params.Cursor, total)
	end := min(start+params.Limit, total)
	page := res.Keys[start:end]

	nextCursor := ""
	if end < total {
		nextCursor = strconv.Itoa(end)
	}

	return c.JSON(listKeysResponse{
		Keys:         page,
		NextCursor:   nextCursor,
		TotalMatched: total,
		Truncated:    res.Truncated,
		Node:         nodeCtx.nodeID,
		PartialNodes: res.PartialNodes,
	})
}

// meResponse is the body of GET /v1/me — the resolved caller identity
// after auth middleware ran. Mirrors httpauth.Identity but written as
// a wire type so the JSON tags are owned by the API surface, not the
// internal auth package.
//
// Capabilities is the stable-string view of what the caller can DO
// (vs Scopes, which is the storage-shape of what the caller HAS).
// Today the mapping is 1:1 — every scope produces one capability
// prefixed `cache.` — but the indirection lets us split a scope
// without breaking clients that key off capability strings. See
// httpauth.Identity.Capabilities() for the derivation.
type meResponse struct {
	ID           string   `json:"id"`
	Scopes       []string `json:"scopes"`
	Capabilities []string `json:"capabilities"`
}

// handleMe implements GET /v1/me — returns the calling principal's
// identity and granted scopes. Used by the monitor to introspect a
// bound bearer token without making a no-op probe against another
// route. The middleware has already populated IdentityKey on every
// request that reached this handler (anonymous mode included), so
// the type assertion is safe; a missing or wrong-typed Locals entry
// is a wiring bug and surfaces as a 500 rather than a silent default.
func handleMe(c fiber.Ctx) error {
	identity, ok := c.Locals(httpauth.IdentityKey).(httpauth.Identity)
	if !ok {
		return jsonErr(
			c,
			fiber.StatusInternalServerError,
			codeInternal,
			"identity not resolved by middleware (wiring bug)",
		)
	}

	scopes := make([]string, len(identity.Scopes))
	for i, s := range identity.Scopes {
		scopes[i] = string(s)
	}

	return c.JSON(meResponse{
		ID:           identity.ID,
		Scopes:       scopes,
		Capabilities: identity.Capabilities(),
	})
}

// canResponse is the body of GET /v1/me/can?capability=<name>.
// `Allowed` is the discrimination result; `Capability` echoes the
// caller's input so log scraping ties allow/deny to the asked
// capability without parsing the query string again.
type canResponse struct {
	Capability string `json:"capability"`
	Allowed    bool   `json:"allowed"`
}

// capability strings — closed set in the `cache.` namespace.
// Unknown values return 400 rather than silently false so callers
// detect typos instead of shipping broken authz logic to prod.
const (
	capabilityCacheRead  = "cache.read"
	capabilityCacheWrite = "cache.write"
	capabilityCacheAdmin = "cache.admin"
)

// isKnownCapability reports whether s is one of the three
// recognized capability strings. Switch-based so a future
// capability is one named const + one case.
func isKnownCapability(s string) bool {
	switch s {
	case capabilityCacheRead, capabilityCacheWrite, capabilityCacheAdmin:
		return true
	default:
		return false
	}
}

// handleCan implements GET /v1/me/can?capability=cache.write —
// per-capability authorization probe. Caller passes a capability
// string; the response says whether the resolved identity holds
// it. Cheaper than the speculative-write pattern (try the write,
// catch the 403), and stable across future scope-to-capability
// refactors (clients key off the capability string, not the
// internal scope shape).
//
// Requires the `read` scope — same threshold as /v1/me. Unknown
// capability values fail BAD_REQUEST so typos don't silently
// answer "not allowed" when the real issue is the caller's
// spelling.
func handleCan(c fiber.Ctx) error {
	capability := c.Query("capability")
	if capability == "" {
		return jsonErr(c, fiber.StatusBadRequest, codeBadRequest, "missing 'capability' query parameter")
	}

	if !isKnownCapability(capability) {
		return jsonErr(c, fiber.StatusBadRequest, codeBadRequest, "unknown capability '"+capability+"'")
	}

	identity, ok := c.Locals(httpauth.IdentityKey).(httpauth.Identity)
	if !ok {
		return jsonErr(
			c,
			fiber.StatusInternalServerError,
			codeInternal,
			"identity not resolved by middleware (wiring bug)",
		)
	}

	return c.JSON(canResponse{
		Capability: capability,
		Allowed:    identity.HasCapability(capability),
	})
}

func main() { os.Exit(run()) }

// run is the testable main body — separated so deferred cleanup
// (context cancel, future cleanups) executes before process exit.
// Returns 0 on clean shutdown, 1 on construction failure.
func run() int {
	cfg, err := loadConfig()
	if err != nil {
		// Fall back to a minimal stderr logger because cfg.LogLevel
		// is not yet populated. Auth-policy load errors are
		// fail-closed: missing/malformed HYPERCACHE_AUTH_CONFIG
		// must not silently degrade to open mode.
		fmt.Fprintf(os.Stderr, "hypercache-server: %v\n", err)

		return 1
	}

	baseLogger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	logger := baseLogger.With(slog.String("node_id", cfg.NodeID))

	slog.SetDefault(logger)

	// Preserve the pre-v2 zero-config dev posture: when neither
	// HYPERCACHE_AUTH_CONFIG nor HYPERCACHE_AUTH_TOKEN is set, the
	// loader returns the zero Policy, and we explicitly opt into
	// AllowAnonymous mode here with a loud warning. Without this,
	// every protected route would 401 — every existing
	// `docker run hypercache` would break on upgrade.
	if !cfg.AuthPolicy.IsConfigured() {
		logger.Warn(
			"hypercache-server running with no client API auth configured; set " +
				httpauth.EnvAuthConfig + " or " + httpauth.EnvAuthToken + " for production",
		)

		cfg.AuthPolicy.AllowAnonymous = true
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if !attachOIDCVerifier(ctx, &cfg, logger) {
		return 1
	}

	logger.Info(
		"hypercache-server starting",
		slog.String("api_addr", cfg.APIAddr),
		slog.String("mgmt_addr", cfg.MgmtAddr),
		slog.String("dist_addr", cfg.DistAddr),
		slog.Any("seeds", cfg.Seeds),
		slog.Int("replication", cfg.Replication),
		slog.Int("auth_token_identities", len(cfg.AuthPolicy.Tokens)),
		slog.Int("auth_cert_identities", len(cfg.AuthPolicy.CertIdentities)),
		slog.Bool("oidc_enabled", cfg.AuthPolicy.ServerVerify != nil),
	)

	hc, err := buildHyperCache(ctx, cfg, logger)
	if err != nil {
		logger.Error("hypercache construction failed", slog.Any("err", err))

		return 1
	}

	apiApp, err := runClientAPI(cfg, hc, logger)
	if err != nil {
		logger.Error("client API construction failed", slog.Any("err", err))

		_ = hc.Stop(ctx)

		return 1
	}

	awaitShutdown(ctx, hc, apiApp, logger)

	return 0
}

// awaitShutdown blocks until SIGTERM/SIGINT, then runs the graceful
// drain sequence: drain dist (so /health 503s and writes return
// ErrDraining), shut down client API, then stop the cache (which
// also stops the management HTTP server). A 30s timeout caps the
// whole sequence so a misbehaving listener can't block forever.
func awaitShutdown(ctx context.Context, hc *hypercache.HyperCache[backend.DistMemory], apiApp *fiber.App, logger *slog.Logger) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)

	sig := <-sigs
	logger.Info("shutdown signal received", slog.String("signal", sig.String()))

	shutdownCtx, cancel := context.WithTimeout(ctx, shutdownDeadline)
	defer cancel()

	drainErr := hc.DistDrain(shutdownCtx)
	if drainErr != nil {
		logger.Warn("drain returned error", slog.Any("err", drainErr))
	}

	err := apiApp.ShutdownWithContext(shutdownCtx)
	if err != nil {
		logger.Warn("client API shutdown returned error", slog.Any("err", err))
	}

	err = hc.Stop(shutdownCtx)
	if err != nil {
		logger.Warn("hypercache stop returned error", slog.Any("err", err))
	}

	logger.Info("hypercache-server stopped cleanly")
}
