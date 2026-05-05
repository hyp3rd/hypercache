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
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	fiber "github.com/gofiber/fiber/v3"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/internal/sentinel"
	"github.com/hyp3rd/hypercache/pkg/backend"
)

// Defaults applied when the corresponding env var is unset. Centralized
// here so operators see one canonical reference and so the magic-number
// linter doesn't flag repeated literals at the env-parse sites.
const (
	defaultReplication    = 3
	defaultCapacity       = 100_000
	defaultVirtualNodes   = 64
	defaultIndirectK      = 2
	suspectMultiplier     = 3 // suspect after = N × heartbeat interval
	deadMultiplier        = 6 // dead    after = N × heartbeat interval
	defaultHintTTL        = 30 * time.Second
	defaultHintReplay     = 200 * time.Millisecond
	defaultHeartbeat      = 1 * time.Second
	defaultRebalance      = 250 * time.Millisecond
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
	AuthToken    string
	LogLevel     slog.Level
	HintTTL      time.Duration
	HintReplay   time.Duration
	Heartbeat    time.Duration
	IndirectK    int
	RebalanceInt time.Duration
}

// loadConfig pulls every knob from the environment and applies sane
// defaults. Returns the parsed config and any non-fatal warnings the
// caller should log after the logger is wired.
func loadConfig() envConfig {
	cfg := envConfig{
		NodeID:       envOr("HYPERCACHE_NODE_ID", hostnameOrDefault()),
		APIAddr:      envOr("HYPERCACHE_API_ADDR", ":8080"),
		MgmtAddr:     envOr("HYPERCACHE_MGMT_ADDR", ":8081"),
		DistAddr:     envOr("HYPERCACHE_DIST_ADDR", ":7946"),
		Seeds:        splitCSV(os.Getenv("HYPERCACHE_SEEDS")),
		Replication:  envInt("HYPERCACHE_REPLICATION", defaultReplication),
		Capacity:     envInt("HYPERCACHE_CAPACITY", defaultCapacity),
		AuthToken:    os.Getenv("HYPERCACHE_AUTH_TOKEN"),
		LogLevel:     parseLogLevel(envOr("HYPERCACHE_LOG_LEVEL", "info")),
		HintTTL:      envDuration("HYPERCACHE_HINT_TTL", defaultHintTTL),
		HintReplay:   envDuration("HYPERCACHE_HINT_REPLAY", defaultHintReplay),
		Heartbeat:    envDuration("HYPERCACHE_HEARTBEAT", defaultHeartbeat),
		IndirectK:    envInt("HYPERCACHE_INDIRECT_PROBE_K", defaultIndirectK),
		RebalanceInt: envDuration("HYPERCACHE_REBALANCE_INTERVAL", defaultRebalance),
	}

	return cfg
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
		backend.WithDistHintTTL(cfg.HintTTL),
		backend.WithDistHintReplayInterval(cfg.HintReplay),
		backend.WithDistRebalanceInterval(cfg.RebalanceInt),
		backend.WithDistLogger(logger),
	}

	if cfg.AuthToken != "" {
		hcCfg.DistMemoryOptions = append(
			hcCfg.DistMemoryOptions,
			backend.WithDistHTTPAuth(backend.DistHTTPAuth{Token: cfg.AuthToken}),
		)
	}

	hcCfg.HyperCacheOptions = append(
		hcCfg.HyperCacheOptions,
		hypercache.WithManagementHTTP[backend.DistMemory](cfg.MgmtAddr),
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

// runClientAPI builds and starts the client REST API. Returns the
// fiber app so main can shut it down on signal. Handlers are
// auth-wrapped when the env carries an HYPERCACHE_AUTH_TOKEN, mirroring
// the dist + management HTTP auth posture.
func runClientAPI(addr, nodeID string, hc *hypercache.HyperCache[backend.DistMemory], authToken string, logger *slog.Logger) *fiber.App {
	app := fiber.New(fiber.Config{
		AppName:      "hypercache-server",
		ReadTimeout:  clientAPIReadTimeout,
		WriteTimeout: clientAPIWriteTimeout,
		IdleTimeout:  clientAPIIdleTimeout,
	})

	auth := bearerAuth(authToken)
	nodeCtx := &nodeContext{hc: hc, nodeID: nodeID}

	app.Get("/healthz", func(c fiber.Ctx) error { return c.SendString("ok") })

	app.Put("/v1/cache/:key", auth(func(c fiber.Ctx) error { return handlePut(c, nodeCtx) }))
	app.Get("/v1/cache/:key", auth(func(c fiber.Ctx) error { return handleGet(c, nodeCtx) }))
	app.Delete("/v1/cache/:key", auth(func(c fiber.Ctx) error { return handleDelete(c, nodeCtx) }))
	app.Get("/v1/owners/:key", auth(func(c fiber.Ctx) error { return handleOwners(c, nodeCtx) }))

	go func() {
		err := app.Listen(addr)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("client API listener exited", slog.Any("err", err))
		}
	}()

	return app
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

// handleGet implements GET /v1/cache/:key — returns the raw bytes
// with Content-Type application/octet-stream, or a JSON 404 when
// the key is absent. JSON-on-error keeps the response shape
// machine-friendly even when the value path returns raw bytes.
func handleGet(c fiber.Ctx, nodeCtx *nodeContext) error {
	key := c.Params("key")
	if key == "" {
		return jsonErr(c, fiber.StatusBadRequest, codeBadRequest, "missing key in path")
	}

	v, ok := nodeCtx.hc.Get(c.Context(), key)
	if !ok {
		return jsonErr(c, fiber.StatusNotFound, codeNotFound, "key not found")
	}

	return writeValue(c, v)
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

// bearerAuth returns a middleware that requires `Authorization: Bearer
// <token>` when token is non-empty; otherwise it's a passthrough.
// Mirrors the same posture as DistHTTPAuth — applied to the client
// API for symmetry.
func bearerAuth(token string) func(fiber.Handler) fiber.Handler {
	if token == "" {
		return func(h fiber.Handler) fiber.Handler { return h }
	}

	want := "Bearer " + token

	return func(h fiber.Handler) fiber.Handler {
		return func(c fiber.Ctx) error {
			got := c.Get("Authorization")
			if got != want {
				return c.SendStatus(fiber.StatusUnauthorized)
			}

			return h(c)
		}
	}
}

func main() { os.Exit(run()) }

// run is the testable main body — separated so deferred cleanup
// (context cancel, future cleanups) executes before process exit.
// Returns 0 on clean shutdown, 1 on construction failure.
func run() int {
	cfg := loadConfig()

	baseLogger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	logger := baseLogger.With(slog.String("node_id", cfg.NodeID))

	slog.SetDefault(logger)

	logger.Info(
		"hypercache-server starting",
		slog.String("api_addr", cfg.APIAddr),
		slog.String("mgmt_addr", cfg.MgmtAddr),
		slog.String("dist_addr", cfg.DistAddr),
		slog.Any("seeds", cfg.Seeds),
		slog.Int("replication", cfg.Replication),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hc, err := buildHyperCache(ctx, cfg, logger)
	if err != nil {
		logger.Error("hypercache construction failed", slog.Any("err", err))

		return 1
	}

	apiApp := runClientAPI(cfg.APIAddr, cfg.NodeID, hc, cfg.AuthToken, logger)

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
