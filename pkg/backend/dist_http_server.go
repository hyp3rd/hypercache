package backend

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/goccy/go-json"
	fiber "github.com/gofiber/fiber/v3"
	"github.com/hyp3rd/ewrap"

	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/internal/sentinel"
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
	// auth is the configured authentication policy. Zero-valued means no
	// auth (current default behavior).
	auth DistHTTPAuth
	// tlsConfig (when non-nil) wraps the listener with tls.NewListener.
	// Resolver advertises https:// in that case; clients with the same
	// TLSConfig handshake successfully, plaintext peers are rejected at
	// the TCP level by Go's TLS server.
	tlsConfig *tls.Config
	// serveErr captures the last error returned by app.Listener when the
	// background serve goroutine exits. Operators can read it via
	// LastServeError() to surface listener failures (e.g. port already in
	// use, TLS handshake failure on accept) instead of having them
	// silently swallowed.
	serveErr atomic.Pointer[error]
	// logger is the structured logger inherited from the parent
	// DistMemory. Used to surface serve-goroutine errors that previously
	// only landed in serveErr (LastServeError accessor) — operators
	// running with a configured logger now see them in their log stream
	// at the moment of failure, not just on demand.
	logger *slog.Logger
}

// DistHTTPAuth configures authentication for the dist HTTP server
// (inbound) and the auto-created HTTP client (outbound). The two sides
// are independent: ServerVerify+Token govern inbound validation, while
// ClientSign+Token govern outbound signing. Zero-value disables both.
//
// Symmetric clusters use Token alone: every node sets the same string,
// the server validates incoming `Authorization: Bearer <token>` via
// constant-time compare, and the client sends the same header.
//
// ServerVerify (inbound) and ClientSign (outbound) are escape hatches
// for JWT, mTLS-derived identity, HMAC signing, etc. When set, each
// fully replaces the corresponding Token-based default on its side.
//
// Asymmetric configs are valid but require explicit intent. In
// particular, setting ClientSign without any inbound verifier (Token
// or ServerVerify) is dangerous — the node would sign outbound traffic
// while accepting unauthenticated inbound. NewDistMemory rejects that
// shape with sentinel.ErrInsecureAuthConfig. Operators who genuinely
// want signed-out / open-in (e.g. inbound is gated by an L4 firewall
// or service mesh) must opt in via AllowAnonymousInbound.
type DistHTTPAuth struct {
	// Token is the shared bearer string. When set, the server requires
	// `Authorization: Bearer <token>` on every request (unless
	// ServerVerify overrides) and the auto-created client sends the
	// same header (unless ClientSign overrides).
	Token string
	// ServerVerify (optional) inspects each incoming request and returns
	// non-nil to reject with HTTP 401. Use for JWT, OAuth introspection,
	// path-based exemptions, etc. When set it replaces the Token check.
	ServerVerify func(fiber.Ctx) error
	// ClientSign (optional) decorates each outgoing request before send.
	// Use for HMAC signing, mTLS-derived headers, etc. When set it
	// replaces the default `Authorization: Bearer <token>` header.
	ClientSign func(*http.Request) error
	// AllowAnonymousInbound permits this node to accept inbound requests
	// without authentication when no inbound verifier is configured
	// (neither Token nor ServerVerify) but ClientSign is. Without this
	// flag, that combination is rejected at construction time to prevent
	// silent inbound bypass when an operator wires only one side of an
	// HMAC scheme. Setting this flag is an explicit acknowledgment that
	// inbound traffic is protected at a layer below this server (L4
	// firewall, service mesh mTLS, etc.).
	AllowAnonymousInbound bool
}

// inboundConfigured reports whether server-side validation is active —
// drives whether incoming requests are auth-checked. ClientSign alone
// does NOT count: it is an outbound concern. Outbound signing has no
// equivalent predicate because sign() is already path-specific (it
// short-circuits when both Token and ClientSign are zero).
func (a DistHTTPAuth) inboundConfigured() bool {
	return a.Token != "" || a.ServerVerify != nil
}

// validate enforces the inbound/outbound coherence rules at construction
// time. Returns sentinel.ErrInsecureAuthConfig when ClientSign is set
// without any inbound verifier and the operator has not explicitly
// opted into anonymous inbound — the configuration shape that previously
// caused a silent inbound auth bypass.
func (a DistHTTPAuth) validate() error {
	signOnly := a.ClientSign != nil && !a.inboundConfigured()
	if signOnly && !a.AllowAnonymousInbound {
		return sentinel.ErrInsecureAuthConfig
	}

	return nil
}

// verify validates the incoming request against the configured inbound
// policy. Returns nil when the request is authorized, non-nil otherwise.
// The default (Token-only) check uses constant-time compare to defeat
// timing side-channels. Callers must gate this behind inboundConfigured()
// — verify itself returns nil when no inbound check is configured, which
// is the intended behavior only when inbound is deliberately open.
func (a DistHTTPAuth) verify(fctx fiber.Ctx) error {
	if a.ServerVerify != nil {
		return a.ServerVerify(fctx)
	}

	if a.Token == "" {
		return nil
	}

	got := fctx.Get("Authorization")
	want := "Bearer " + a.Token

	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return sentinel.ErrUnauthorized
	}

	return nil
}

// sign decorates an outgoing request with the configured auth header.
// Default (Token-only) sets `Authorization: Bearer <token>`. ClientSign
// fully overrides when set.
func (a DistHTTPAuth) sign(req *http.Request) error {
	if a.ClientSign != nil {
		return a.ClientSign(req)
	}

	if a.Token == "" {
		return nil
	}

	req.Header.Set("Authorization", "Bearer "+a.Token)

	return nil
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
	// TLSConfig (when non-nil) enables TLS for both the dist HTTP server
	// (wraps the TCP listener with tls.NewListener) and the auto-created
	// HTTP client (sets Transport.TLSClientConfig). Operators must apply
	// the same config to every node; mismatched roots/certs cause peer
	// handshakes to fail. The same struct is shared by server and client
	// because in this codebase a node is both — but tests / advanced
	// callers can fork the value and assign different ones if needed.
	//
	// For mTLS, set both Certificates (server cert) and ClientCAs +
	// ClientAuth=tls.RequireAndVerifyClientCert. The auto-client uses
	// the same cert as its client cert via Certificates[0].
	TLSConfig *tls.Config

	// CompressionThreshold opts the dist HTTP transport into gzip
	// compression of Set request bodies whose serialized payload size
	// exceeds this many bytes. The client sets `Content-Encoding:
	// gzip` and the server transparently decompresses before
	// unmarshaling. 0 disables compression — matches the pre-Phase-B
	// wire format byte-for-byte. Operators on bandwidth-constrained
	// links with large values (>1 KiB) typically see meaningful
	// reductions; values smaller than the threshold pay no cost.
	//
	// Server compatibility: a server with compression disabled will
	// reject a gzip-encoded body with HTTP 400. Roll out the threshold
	// to all peers before raising it on any peer.
	CompressionThreshold int
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

func newDistHTTPServer(addr string, limits DistHTTPLimits, auth DistHTTPAuth) *distHTTPServer {
	limits = limits.withDefaults()

	app := fiber.New(fiber.Config{
		ReadTimeout:  limits.ReadTimeout,
		WriteTimeout: limits.WriteTimeout,
		IdleTimeout:  limits.IdleTimeout,
		BodyLimit:    limits.BodyLimit,
		Concurrency:  limits.Concurrency,
	})

	return &distHTTPServer{app: app, addr: addr, auth: auth, tlsConfig: limits.TLSConfig}
}

// LastServeError returns the last error captured from the background
// serve goroutine (typically Listener accept-loop failure or TLS-level
// rejection). Returns nil when the server shut down cleanly. Replaces
// the pre-5e silent-swallow pattern where serveErr was assigned to _ and
// dropped, leaving operators with no signal that the listener died.
func (s *distHTTPServer) LastServeError() error {
	if s == nil {
		return nil
	}

	if errp := s.serveErr.Load(); errp != nil {
		return *errp
	}

	return nil
}

// wrapAuth returns an auth-checking wrapper around the supplied handler
// when the server's *inbound* auth policy is configured; otherwise
// returns the handler untouched (zero overhead for unauthenticated
// deployments). Outbound-only configs (ClientSign without Token or
// ServerVerify) intentionally fall through to the bare handler — that
// shape is rejected at NewDistMemory unless AllowAnonymousInbound is
// set, which is the operator's explicit acknowledgment that inbound is
// protected by a layer below this server.
func (s *distHTTPServer) wrapAuth(handler fiber.Handler) fiber.Handler {
	if !s.auth.inboundConfigured() {
		return handler
	}

	return func(fctx fiber.Ctx) error {
		err := s.auth.verify(fctx)
		if err != nil {
			return fctx.Status(fiber.StatusUnauthorized).JSON(fiber.Map{constants.ErrorLabel: err.Error()})
		}

		return handler(fctx)
	}
}

// start registers handlers and binds the listener. The caller MUST set
// s.ctx to the desired handler-side lifecycle context before calling
// start — that ctx is captured into handler closures and used as the
// operation ctx for backend ops (applySet, applyRemove). The bindCtx
// argument controls only the listener.Listen call.
func (s *distHTTPServer) start(bindCtx context.Context, dm *DistMemory) error {
	if s.ctx == nil {
		// Defensive default: fall back to the bind ctx so the server is
		// usable even if the caller forgot to set a lifecycle ctx. This
		// matches the pre-5d behavior where start captured its own ctx.
		s.ctx = bindCtx
	}

	s.registerSet(dm)
	s.registerGet(dm)
	s.registerRemove(dm)
	s.registerHealth(dm)
	s.registerDrain(dm)
	s.registerProbe(dm)
	s.registerGossip(dm)
	s.registerMerkle(dm)

	return s.listen(bindCtx)
}

// handleSet decodes a httpSetRequest and applies it locally + optionally
// fan-outs to replicas. Uses s.ctx (server-lifecycle) as the backend
// operation context — see the comment on distHTTPServer.ctx for why we
// can't use the per-request fiber.Ctx here.
//
// Compression note: fiber v3's Body() auto-decompresses based on the
// inbound `Content-Encoding` header, so this handler does not need
// explicit gzip handling — it sees the plaintext JSON regardless of
// whether the client compressed (CompressionThreshold > 0) or not.
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
		LastAccess:  time.Now(),
	}

	// Forwarded arrival from a peer: ownership guard fires if this
	// node disagrees with the sender's view about K's owners.
	dm.applyForwardedSet(s.ctx, it, req.Replicate)

	return fctx.JSON(httpSetResponse{})
}

func (s *distHTTPServer) registerSet(dm *DistMemory) {
	handler := s.wrapAuth(func(fctx fiber.Ctx) error { return s.handleSet(fctx, dm) })
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
	handler := s.wrapAuth(func(fctx fiber.Ctx) error { return s.handleGet(fctx, dm) })
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
	handler := s.wrapAuth(func(fctx fiber.Ctx) error { return s.handleRemove(fctx, dm) })
	s.app.Delete("/internal/cache/remove", handler)
	s.app.Delete("/internal/del", handler)
}

func (s *distHTTPServer) registerHealth(dm *DistMemory) {
	// Auth-wrapped: when a token is configured, /health requires it too.
	// Operators who want a public health probe should supply a custom
	// ServerVerify that exempts the /health path.
	//
	// Drain semantics: when dm.IsDraining() is true the endpoint
	// returns HTTP 503 with body "draining" so external load balancers
	// stop routing traffic. The drain check fires before the
	// always-ok response so a draining node never falsely advertises
	// readiness.
	s.app.Get("/health", s.wrapAuth(func(fctx fiber.Ctx) error {
		if dm.IsDraining() {
			return fctx.Status(fiber.StatusServiceUnavailable).SendString("draining")
		}

		return fctx.SendString("ok")
	}))
}

// registerDrain wires `POST /dist/drain` — the operator-driven
// graceful-shutdown trigger. Auth-wrapped because draining is a
// privileged action: any peer that can call it can stall this node's
// writes. Returns 200 on the first successful transition; idempotent
// follow-up calls also return 200 (drain is one-way per Drain doc).
func (s *distHTTPServer) registerDrain(dm *DistMemory) {
	s.app.Post("/dist/drain", s.wrapAuth(func(fctx fiber.Ctx) error {
		err := dm.Drain(s.ctx)
		if err != nil {
			return fctx.Status(fiber.StatusInternalServerError).JSON(fiber.Map{constants.ErrorLabel: err.Error()})
		}

		return fctx.JSON(fiber.Map{"draining": true})
	}))
}

// registerProbe wires `/internal/probe?target=<id>` — the indirect-probe
// relay endpoint used by the SWIM heartbeat path. The relay node calls
// its own transport's Health(target) and reports the result. 200 = relay
// reached the target; 502 = relay's probe failed; 404 = target unknown
// to the relay; 400 = missing/empty target query parameter. Auth-wrapped
// like the rest of `/internal/*` because indirectly probing arbitrary
// node IDs through a member is a directory-enumeration vector.
func (s *distHTTPServer) registerProbe(dm *DistMemory) {
	s.app.Get("/internal/probe", s.wrapAuth(func(fctx fiber.Ctx) error {
		target := fctx.Query("target")
		if target == "" {
			return fctx.Status(fiber.StatusBadRequest).JSON(fiber.Map{constants.ErrorLabel: "missing target"})
		}

		transport := dm.loadTransport()
		if transport == nil {
			return fctx.SendStatus(fiber.StatusServiceUnavailable)
		}

		err := transport.Health(s.ctx, target)
		if err != nil {
			if errors.Is(err, sentinel.ErrBackendNotFound) {
				return fctx.SendStatus(fiber.StatusNotFound)
			}

			return fctx.SendStatus(fiber.StatusBadGateway)
		}

		return fctx.SendString("ok")
	}))
}

// registerGossip wires `POST /internal/gossip` — the SWIM
// membership-dissemination endpoint. The body is a JSON array of
// GossipMember snapshots; the receiver's acceptGossip merges them
// via higher-incarnation-wins and self-refutes if any entry
// claims this node is suspect or dead.
//
// Auth-wrapped like the rest of `/internal/*` because gossip can
// inject membership state — an unauthenticated peer could mark
// real nodes as dead by spoofing a high-incarnation snapshot.
func (s *distHTTPServer) registerGossip(dm *DistMemory) {
	s.app.Post("/internal/gossip", s.wrapAuth(func(fctx fiber.Ctx) error {
		var members []GossipMember

		err := json.Unmarshal(fctx.Body(), &members)
		if err != nil {
			return fctx.Status(fiber.StatusBadRequest).JSON(fiber.Map{constants.ErrorLabel: err.Error()})
		}

		dm.acceptGossip(gossipMembersToNodes(members))

		return fctx.SendStatus(fiber.StatusOK)
	}))
}

func (s *distHTTPServer) registerMerkle(dm *DistMemory) {
	s.app.Get("/internal/merkle", s.wrapAuth(func(fctx fiber.Ctx) error {
		tree := dm.BuildMerkleTree()

		return fctx.JSON(fiber.Map{
			"root":        tree.Root,
			"leaf_hashes": tree.LeafHashes,
			"chunk_size":  tree.ChunkSize,
		})
	}))

	s.app.Get("/internal/keys", s.wrapAuth(func(fctx fiber.Ctx) error {
		return handleKeys(fctx, dm)
	}))
}

// handleKeys serves shard-level cursor pagination for the
// `/internal/keys` endpoint. Pre-Phase-C this returned every key in
// the cluster in one response — fine for test fixtures, infeasible
// for any real workload. The cursor is the *next* shard index to
// read; an absent cursor starts at 0, an empty `next_cursor` in the
// response signals end-of-iteration.
//
// Pagination granularity is per-shard rather than per-key on
// purpose. ConcurrentMap.All() iterates in unspecified order, so a
// per-key cursor would either need a stable sort (materializing the
// full key set defeats the pagination) or session state on the
// server. Per-shard pagination is bounded by shard size (typically
// well under a million keys) and matches the natural unit of work.
//
// Operators with shards larger than the page-size cap can use the
// `limit` query parameter to truncate within a shard — the response
// then carries an unchanged `next_cursor` and a `truncated` flag so
// the client knows the same shard still has more keys. The simple
// case (no limit) returns the full shard.
func handleKeys(fctx fiber.Ctx, dm *DistMemory) error {
	cursor := 0

	if raw := fctx.Query("cursor"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			return fctx.Status(fiber.StatusBadRequest).JSON(fiber.Map{constants.ErrorLabel: "invalid cursor"})
		}

		cursor = parsed
	}

	limit := 0

	if raw := fctx.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			return fctx.Status(fiber.StatusBadRequest).JSON(fiber.Map{constants.ErrorLabel: "invalid limit"})
		}

		limit = parsed
	}

	// Optional filter: empty `q` keeps the pre-existing
	// return-everything semantic for callers that haven't been
	// upgraded (notably the merkle anti-entropy path). A non-empty
	// pattern containing glob metacharacters (* ? [) is matched via
	// path.Match; anything else is treated as a literal prefix.
	matcher, mErr := buildKeyMatcher(fctx.Query("q"))
	if mErr != nil {
		return fctx.Status(fiber.StatusBadRequest).JSON(fiber.Map{constants.ErrorLabel: mErr.Error()})
	}

	if cursor >= len(dm.shards) {
		return fctx.JSON(fiber.Map{"keys": []string{}, "next_cursor": ""})
	}

	shard := dm.shards[cursor]
	if shard == nil { // skip nil shards (defensive)
		return fctx.JSON(fiber.Map{"keys": []string{}, "next_cursor": strconv.Itoa(cursor + 1)})
	}

	keys, truncated := collectShardKeys(shard, limit, matcher)

	nextCursor := ""

	switch {
	case truncated:
		// Same shard still has keys past the limit; client must
		// re-request with a larger limit (per-shard pagination doesn't
		// resume mid-shard, which would require session state).
		nextCursor = strconv.Itoa(cursor)

	case cursor+1 < len(dm.shards):
		nextCursor = strconv.Itoa(cursor + 1)

	default:
		// Last shard fully drained — leave nextCursor empty to signal
		// end-of-iteration to the client.
	}

	return fctx.JSON(fiber.Map{
		"keys":        keys,
		"next_cursor": nextCursor,
		"truncated":   truncated,
	})
}

// collectShardKeys reads up to `limit` keys from shard that match
// `matcher`. limit<=0 returns every matching key. The truncated
// bool reports whether the shard had more matching keys than
// `limit` allowed.
//
// Matcher is applied during iteration so non-matching keys never
// count against the limit — a `limit=10, q="first-*"` request
// returns the first 10 keys that match the prefix/glob, not
// "the first 10 keys, of which some happen to match.".
func collectShardKeys(shard *distShard, limit int, matcher func(string) bool) ([]string, bool) {
	out := make([]string, 0, shard.items.Count())

	truncated := false

	for k := range shard.items.All() {
		if !matcher(k) {
			continue
		}

		if limit > 0 && len(out) >= limit {
			truncated = true

			break
		}

		out = append(out, k)
	}

	return out, truncated
}

func (s *distHTTPServer) listen(ctx context.Context) error {
	lc := net.ListenConfig{}

	ln, err := lc.Listen(ctx, "tcp", s.addr)
	if err != nil {
		return ewrap.Wrap(err, "dist http listen")
	}

	// Wrap the TCP listener with TLS when configured. Plaintext peers
	// connecting to a TLS-wrapped listener fail at the handshake — Go
	// returns a tls.RecordHeaderError to the accept loop, which we
	// capture in serveErr below.
	if s.tlsConfig != nil {
		ln = tls.NewListener(ln, s.tlsConfig)
	}

	s.ln = ln

	go func() {
		// DisableStartupMessage avoids fiber's per-instance banner spam,
		// which would otherwise flood test output at -count=N (see hundreds
		// of "INFO Server started on..." lines drowning real failures).
		serveErr := s.app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true})
		if serveErr != nil {
			// Stash so operators can read it via LastServeError(); a
			// listener that crashed silently is the worst kind of
			// production bug. Also surface to the structured logger when
			// configured so the failure shows up in the operator's log
			// stream at the moment it happens, not just on demand.
			s.serveErr.Store(&serveErr)

			if s.logger != nil {
				s.logger.Error(
					"dist HTTP serve goroutine exited",
					slog.String("addr", s.addr),
					slog.Any("err", serveErr),
				)
			}
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
