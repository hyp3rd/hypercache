// Package httpauth provides authentication policy for the
// hypercache-server client REST API. It is the v2 successor to the
// single-token bearerAuth helper that previously lived inside
// cmd/hypercache-server: that helper supports exactly one shared token
// with no per-identity granularity and no extension hooks. Real
// production deployments need multiple tokens (one per consuming
// service or operator), per-identity scopes (read-only vs read-write
// vs admin), mTLS as a peer mechanism to bearer auth, and a custom
// verify hook for JWT/OAuth/etc.
//
// The package is independent of the dist transport's DistHTTPAuth
// (pkg/backend/dist_http_server.go). Dist auth is intentionally
// symmetric — every node carries the same token because the cluster
// is one trust domain — so multi-identity has no operator meaning
// there. Client API auth is asymmetric (many callers, one server)
// and benefits from the multi-identity shape this package provides.
//
// Wire-shape: a Policy is loaded once at process start (typically
// from HYPERCACHE_AUTH_CONFIG / HYPERCACHE_AUTH_TOKEN; see loader.go)
// and used to build per-route middleware via Policy.Middleware(scope).
// The middleware's verification path runs in time independent of how
// many tokens are configured and which one matched (if any) — see
// the comment on Middleware for the timing-leak considerations.
package httpauth

import (
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"slices"
	"strings"

	fiber "github.com/gofiber/fiber/v3"
	"github.com/hyp3rd/ewrap"
	"golang.org/x/crypto/bcrypt"

	"github.com/hyp3rd/hypercache/internal/sentinel"
)

// Scope is a coarse permission grant applied to an identity. The
// three-scope model maps cleanly to cache semantics: Read covers
// GET/HEAD/owners-lookup; Write covers PUT/DELETE plus their batch
// equivalents; Admin is reserved for management-plane endpoints (not
// yet wired — see plan §"Out of scope: Unifying management HTTP").
//
// Scopes are inclusive, not hierarchical: an identity granted Write
// does NOT implicitly also have Read. Each route declares the exact
// scope it requires; identities carry the union of scopes they hold.
// Hierarchical inheritance (admin > write > read) was rejected as
// the wrong default — it makes "read-only" tokens impossible without
// inverting the polarity, and operators routinely want a service
// that can write but not read (think: ingest-only metrics writers).
type Scope string

const (
	// ScopeRead permits cache lookups and metadata inspection.
	ScopeRead Scope = "read"
	// ScopeWrite permits cache mutations.
	ScopeWrite Scope = "write"
	// ScopeAdmin permits management-plane operations (cluster
	// control, eviction triggers, etc.). Unused by the client
	// API today; reserved for the management HTTP unification.
	ScopeAdmin Scope = "admin"
)

// Identity is the resolved caller for an authorized request. Stored
// into fiber.Ctx locals under IdentityKey so handlers can attribute
// audit logs / metrics to the calling principal without re-deriving
// it. ID is the human-readable label the operator put in the auth
// config; Scopes is the union of grants for that principal.
type Identity struct {
	ID     string
	Scopes []Scope
}

// HasScope reports whether the identity carries the given scope.
// O(n) over the identity's scope list — n is small (3 max), so
// micro-optimizations like a bitmap are not justified.
func (i Identity) HasScope(s Scope) bool {
	return slices.Contains(i.Scopes, s)
}

// Capabilities returns the stable capability strings derived from
// the identity's scopes. Capabilities are the surface clients
// introspect via GET /v1/me — they describe what the caller can
// DO rather than what scopes they HAVE. The two are 1:1 today
// (one capability per scope, prefixed with `cache.`) but the
// indirection lets us split a scope into multiple capabilities
// later (e.g. ScopeRead → cache.read + cache.metrics) without
// breaking clients that key off capability strings.
func (i Identity) Capabilities() []string {
	if len(i.Scopes) == 0 {
		return []string{}
	}

	out := make([]string, 0, len(i.Scopes))
	for _, s := range i.Scopes {
		out = append(out, "cache."+string(s))
	}

	return out
}

// HasCapability reports whether the identity carries the given
// capability string. Used by the /v1/me/can probe endpoint and by
// the client SDK's mirror method — both want a single
// authoritative check rather than reinventing the
// scope-prefix-to-capability mapping at each call site.
func (i Identity) HasCapability(name string) bool {
	return slices.Contains(i.Capabilities(), name)
}

// TokenIdentity is one bearer-token grant in a Policy. The Token
// field is the raw secret; never log it. ID is what shows up in
// audit logs / Identity.ID after a successful match.
type TokenIdentity struct {
	ID     string
	Token  string
	Scopes []Scope
}

// CertIdentity is one mTLS-cert-based grant in a Policy. SubjectCN
// is matched against tls.ConnectionState.VerifiedChains[0][0].Subject.CommonName
// of the peer certificate. Exact-match only — wildcard CN matching
// invites accidental over-grant and is deferred until a concrete
// operator request justifies the complexity.
type CertIdentity struct {
	SubjectCN string
	Scopes    []Scope
}

// BasicIdentity is one HTTP-Basic-auth grant in a Policy. PasswordBcrypt
// stores the bcrypt-hashed form of the operator's chosen password
// (`bcrypt.GenerateFromPassword` at cost ≥ 10); raw passwords NEVER
// appear in the config file or in process memory beyond the per-
// request verification step.
//
// Username is the wire identifier (sent client-side in
// `Authorization: Basic <base64(username:password)>`); ID is the
// audit identifier that shows up in Identity.ID and downstream logs.
// They MAY be the same string but are kept distinct so operators can
// rename machine-facing usernames without rewriting log queries.
//
// Threat note: bcrypt verification runs on every request that
// presents a Basic header. This is intentionally CPU-bound (default
// cost 10 ≈ 60ms on contemporary hardware). A malicious actor with a
// stream of wrong passwords can therefore burn server CPU; mitigate
// via a fronting rate-limiter or an LB-level connection cap. The
// auth layer does NOT itself rate-limit (see RFC 0003 open
// question 3 for the trade-offs).
type BasicIdentity struct {
	Username       string
	PasswordBcrypt []byte // bcrypt-hashed; raw passwords never live here
	ID             string
	Scopes         []Scope
}

// Policy is the authoritative auth configuration for an HTTP
// listener. Build via the loader in this package or construct
// in-process for tests; pass the same value to every route via
// Middleware.
//
// Policy is value-semantic and safe for concurrent use after
// construction — the slices are read-only after load, the
// ServerVerify hook is the operator's responsibility to make
// goroutine-safe.
type Policy struct {
	// Tokens are the bearer-token identities. Constant-time
	// compared against the Authorization header.
	Tokens []TokenIdentity
	// BasicIdentities are the HTTP-Basic-auth identities.
	// Verified by bcrypt-comparing the password presented in
	// `Authorization: Basic ...` against PasswordBcrypt.
	BasicIdentities []BasicIdentity
	// CertIdentities are the mTLS-cert identities. Resolved
	// from the verified peer cert when TLS is enabled with
	// client-cert verification.
	CertIdentities []CertIdentity
	// ServerVerify (optional) is the universal escape hatch.
	// When set and bearer + cert both miss, the hook is called
	// last; returning a non-error Identity authorizes the
	// request. Use for JWT, OIDC introspection, or any other
	// auth scheme this package doesn't natively support.
	ServerVerify func(fiber.Ctx) (Identity, error)
	// AllowAnonymous permits requests with no credentials at
	// all to pass — they get the empty Identity with no
	// scopes, and only routes requiring no specific scope
	// will accept them. Defaults to false. Used by tests and
	// dev-mode deployments; production should always require
	// at least one credential class.
	AllowAnonymous bool
	// AllowBasicWithoutTLS lets Basic auth verify even when the
	// connection is plaintext. Defaults to false (fails closed:
	// Basic over plaintext leaks the password to every network
	// observer). Operators set this to true ONLY for local dev
	// stacks where TLS termination happens elsewhere or is
	// intentionally skipped. Production must leave this false.
	AllowBasicWithoutTLS bool
}

// IdentityKey is the fiber.Ctx.Locals key under which the resolved
// Identity is stored after a successful auth. Handlers that need
// to attribute audit / metrics can read it back via
// `c.Locals(httpauth.IdentityKey).(httpauth.Identity)`.
//
// Exported as a typed key so users don't have to remember the
// stringly-typed name; the type also prevents accidental collisions
// with unrelated fiber locals.
const IdentityKey = "httpauth.identity"

// IsConfigured reports whether the policy has at least one
// credential class configured. The zero Policy (no tokens, no
// certs, no ServerVerify, AllowAnonymous false) maps to "auth
// disabled" mode — every request passes through. Loaders and the
// hypercache-server binary use this to decide whether to emit a
// "running with no auth" startup warning. Callers should NOT use
// it to gate security checks — Middleware already handles the
// no-credentials-configured fall-through correctly.
func (p Policy) IsConfigured() bool {
	return len(p.Tokens) > 0 ||
		len(p.BasicIdentities) > 0 ||
		len(p.CertIdentities) > 0 ||
		p.ServerVerify != nil
}

// Validate enforces coherence at load time. Returns nil for the
// zero Policy (open mode by virtue of nothing being configured)
// and for any policy with at least one credential class. The
// AllowAnonymous-with-no-credentials shape is intentionally
// permitted: it's how the hypercache-server binary preserves the
// pre-v2 zero-config dev posture (no env vars set → open mode).
//
// validate's failure modes are all caller-error rather than
// runtime-error. Loaders should call this once at startup and exit
// non-zero on failure, never silently continue.
func (p Policy) Validate() error {
	for _, t := range p.Tokens {
		if t.Token == "" {
			return fmt.Errorf("%w: token identity %q has empty token", sentinel.ErrInsecureAuthConfig, t.ID)
		}

		if t.ID == "" {
			return fmt.Errorf("%w: token identity has empty ID (token redacted)", sentinel.ErrInsecureAuthConfig)
		}
	}

	for _, b := range p.BasicIdentities {
		err := validateBasicIdentity(b)
		if err != nil {
			return err
		}
	}

	for _, c := range p.CertIdentities {
		if c.SubjectCN == "" {
			return fmt.Errorf("%w: cert identity has empty subject_cn", sentinel.ErrInsecureAuthConfig)
		}
	}

	return nil
}

// validateBasicIdentity enforces the per-row invariants on one
// BasicIdentity. Extracted from Validate so the parent stays under
// the cognitive-complexity cap; the cap exists for reviewer comfort
// and the split happens to align with one credential class per
// helper.
func validateBasicIdentity(b BasicIdentity) error {
	if b.Username == "" {
		return fmt.Errorf("%w: basic identity has empty username (id redacted)", sentinel.ErrInsecureAuthConfig)
	}

	if b.ID == "" {
		return fmt.Errorf("%w: basic identity %q has empty ID", sentinel.ErrInsecureAuthConfig, b.Username)
	}

	if len(b.PasswordBcrypt) == 0 {
		return fmt.Errorf("%w: basic identity %q has empty password_bcrypt", sentinel.ErrInsecureAuthConfig, b.Username)
	}

	// Cost extraction validates the hash is structurally well-formed
	// (a proper $2a$/$2b$/$2y$ string with a parseable cost field).
	// Caught here so a typo in the YAML fails loudly at boot rather
	// than silently rejecting every Basic auth attempt at runtime.
	_, err := bcrypt.Cost(b.PasswordBcrypt)
	if err != nil {
		return fmt.Errorf("%w: basic identity %q password_bcrypt is not a valid bcrypt hash", sentinel.ErrInsecureAuthConfig, b.Username)
	}

	return nil
}

// Middleware returns a fiber middleware that enforces the policy
// for the given required scope. Order of credential resolution:
//
//  1. Bearer token in Authorization header — constant-time
//     compared against EVERY configured token even on early match,
//     so the count of configured tokens does not leak via timing.
//  2. mTLS verified peer cert (if TLSConnectionState present and
//     VerifiedChains is non-empty) — Subject CN matched against
//     CertIdentities.
//  3. ServerVerify hook (if non-nil) — last-resort escape hatch.
//
// On any successful match the resolved Identity is stored under
// IdentityKey and the next handler runs. On no match the request
// gets 401 Unauthorized with no body — credential-class hints
// (which class missed) are deliberately omitted to avoid handing
// attackers a credential-discovery oracle.
//
// When the policy has no configured credentials AND the route
// requires a scope, every request fails 401 — this is fail-closed
// by design. Operators in dev mode should set AllowAnonymous=true
// to opt into permissive behavior.
//
// When the route requires no specific scope (the empty string is
// passed as `required`), the middleware skips scope-checking but
// still resolves the Identity for handlers that want to attribute
// the call. This shape is currently unused but reserved for routes
// that want any-authenticated-caller semantics.
func (p Policy) Middleware(required Scope) fiber.Handler {
	return func(c fiber.Ctx) error {
		err := p.Verify(c, required)
		if err != nil {
			return err
		}

		return c.Next()
	}
}

// Verify resolves credentials, asserts the required scope, and
// stores the resolved Identity in c.Locals(IdentityKey). Returns
// nil on success; on failure returns a *fiber.Error carrying
// status 401 (no credentials matched) or 403 (credentials matched
// but scope is missing). Fiber's default error handler emits the
// canonical text body for the status code.
//
// Use Verify when integrating with code that owns its own next-
// handler dispatch — e.g. ManagementHTTPServer.WithMgmtAuth and
// WithMgmtControlAuth, which short-circuit on a non-nil return
// from the gate function and never call the wrapped handler.
// Middleware() is thin sugar over Verify() + Next() so the auth
// logic lives in exactly one place.
//
// CRITICAL: do NOT switch to `c.SendStatus(...)` here. SendStatus
// returns nil on success, which would silently fall through to
// the wrapped handler in wrapWithGate-style adapters and the
// downstream handler would write its own success status over the
// 401 body. Returning a *fiber.Error keeps both Middleware and
// the gate adapters fail-closed.
func (p Policy) Verify(c fiber.Ctx, required Scope) error {
	identity, ok := p.resolve(c)
	if !ok {
		return fiber.NewError(fiber.StatusUnauthorized)
	}

	if required != "" && !identity.HasScope(required) {
		return fiber.NewError(fiber.StatusForbidden)
	}

	c.Locals(IdentityKey, identity)

	return nil
}

// resolve walks the credential resolution chain in priority order:
// bearer → mTLS cert → ServerVerify hook → anonymous fallback.
// Returns (Identity, true) on the first successful match. Extracted
// from Middleware so each branch is its own short clause and
// reviewers can audit the ordering at a glance — the chain itself
// is the security-critical part, not the 401/403 status mapping.
func (p Policy) resolve(c fiber.Ctx) (Identity, bool) {
	if id, ok := p.resolveBearer(c.Get("Authorization")); ok {
		return id, true
	}

	if id, ok := p.resolveBasic(c); ok {
		return id, true
	}

	if id, ok := p.resolveCert(c); ok {
		return id, true
	}

	if p.ServerVerify != nil {
		id, err := p.ServerVerify(c)
		if err == nil {
			return id, true
		}
	}

	if p.AllowAnonymous {
		// Anonymous identities receive every scope. AllowAnonymous
		// is the explicit operator opt-in to permissive mode (used
		// by the binary's zero-config dev posture); refusing scoped
		// routes here would 403 every legacy `docker run hypercache`
		// without a paired auth config.
		return Identity{
			ID:     "anonymous",
			Scopes: []Scope{ScopeRead, ScopeWrite, ScopeAdmin},
		}, true
	}

	return Identity{}, false
}

// resolveBearer matches the Authorization header against every
// configured TokenIdentity in constant time per token. CRITICAL:
// the loop runs to completion regardless of when (or whether) a
// match is found — a future contributor MUST NOT add an early
// `break` on match. Doing so would make the wall-clock duration
// of the auth check correlate with the index of the matching
// token, leaking the order of tokens in the config (and, with
// careful timing, the cardinality of the token set).
//
// Returns (zero Identity, false) when no Authorization header is
// present — short-circuiting on the empty case is safe because no
// secret comparison happens; the zero header is publicly observable
// from a network attacker's vantage anyway.
func (p Policy) resolveBearer(authHeader string) (Identity, bool) {
	if authHeader == "" || len(p.Tokens) == 0 {
		return Identity{}, false
	}

	got := []byte(authHeader)
	matched := -1

	for i, t := range p.Tokens {
		want := []byte("Bearer " + t.Token)
		// ConstantTimeCompare returns 0 immediately on
		// length mismatch, but the comparison itself runs
		// in time independent of WHERE the first differing
		// byte lives. We always run the compare; we never
		// `break` after matched is set.
		if subtle.ConstantTimeCompare(got, want) == 1 {
			matched = i
		}
	}

	if matched < 0 {
		return Identity{}, false
	}

	t := p.Tokens[matched]

	return Identity{ID: t.ID, Scopes: t.Scopes}, true
}

// resolveBasic matches the Authorization header against every
// configured BasicIdentity. Returns (zero, false) when:
//
//   - The header is absent or not a Basic scheme.
//   - The base64 payload is malformed or has no `:` separator.
//   - The connection is plaintext AND Policy.AllowBasicWithoutTLS
//     is false (the default, fail-closed posture).
//   - No configured username matches.
//   - The configured username matches but the bcrypt comparison
//     against the presented password fails.
//
// Timing considerations: the bcrypt comparison runs in time
// proportional to bcrypt's cost factor independent of password
// length, which is the property bcrypt is designed for. We do NOT
// iterate the BasicIdentities list to completion (unlike bearer
// tokens) because the username acts as a public selector — leaking
// "this username exists" via timing is not worse than the username
// itself being chosen by the operator and rotated less frequently
// than tokens. Compare against bearer-token's constant-time loop,
// which protects the token VALUE (the secret).
//
// Threat note: bcrypt verification is intentionally CPU-bound
// (cost 10 ≈ 60ms). An attacker presenting a stream of wrong
// passwords burns server CPU; mitigation belongs in a fronting
// rate-limiter or LB connection cap. See RFC 0003 open question 3.
func (p Policy) resolveBasic(c fiber.Ctx) (Identity, bool) {
	if len(p.BasicIdentities) == 0 {
		return Identity{}, false
	}

	creds, ok := parseBasicHeader(c.Get("Authorization"))
	if !ok {
		return Identity{}, false
	}

	// Fail-closed on plaintext unless the operator explicitly opted
	// in. The protocol check happens AFTER header parsing so a
	// caller can never use header presence alone to probe the TLS
	// posture; both paths return the same false.
	if !p.AllowBasicWithoutTLS && tlsConnectionState(c) == nil {
		return Identity{}, false
	}

	for _, b := range p.BasicIdentities {
		if b.Username != creds.Username {
			continue
		}

		err := bcrypt.CompareHashAndPassword(b.PasswordBcrypt, []byte(creds.Password))
		if err != nil {
			return Identity{}, false
		}

		return Identity{ID: b.ID, Scopes: b.Scopes}, true
	}

	return Identity{}, false
}

// basicCreds is the result of parseBasicHeader. Kept as a struct
// rather than two same-typed return values so reviewers don't have
// to remember which positional argument is which (the linter is
// vocal about this — see revive's confusing-results rule).
type basicCreds struct {
	Username string
	Password string
}

// parseBasicHeader decodes an `Authorization: Basic <base64>` header
// into its `username:password` halves. The second return is false
// for any shape problem (missing prefix, bad base64, no colon
// separator) — never panics, never logs, never returns partial data.
func parseBasicHeader(authHeader string) (basicCreds, bool) {
	const prefix = "Basic "

	if !strings.HasPrefix(authHeader, prefix) {
		return basicCreds{}, false
	}

	encoded := strings.TrimSpace(authHeader[len(prefix):])
	if encoded == "" {
		return basicCreds{}, false
	}

	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return basicCreds{}, false
	}

	user, pass, found := strings.Cut(string(decoded), ":")
	if !found {
		return basicCreds{}, false
	}

	return basicCreds{Username: user, Password: pass}, true
}

// resolveCert maps a verified peer certificate to a CertIdentity by
// Subject CN. Requires TLS with client-cert verification — the
// fiber.Ctx must report a tls.ConnectionState with at least one
// VerifiedChain. Unverified or missing chains return (zero, false)
// without checking the configured CertIdentities; we never trust a
// cert chain we did not verify ourselves.
func (p Policy) resolveCert(c fiber.Ctx) (Identity, bool) {
	if len(p.CertIdentities) == 0 {
		return Identity{}, false
	}

	state := tlsConnectionState(c)
	if state == nil || len(state.VerifiedChains) == 0 || len(state.VerifiedChains[0]) == 0 {
		return Identity{}, false
	}

	cn := state.VerifiedChains[0][0].Subject.CommonName
	if cn == "" {
		return Identity{}, false
	}

	for _, ci := range p.CertIdentities {
		if ci.SubjectCN == cn {
			return Identity{ID: cn, Scopes: ci.Scopes}, true
		}
	}

	return Identity{}, false
}

// tlsConnectionState extracts the per-connection TLS state from a
// fiber context, or nil when the request was plaintext. Indirection
// kept here so the test suite can stub it without depending on
// fiber's TLS plumbing — see policy_test.go's stubTLSState helper.
func tlsConnectionState(c fiber.Ctx) *tls.ConnectionState {
	// fiber/fasthttp expose the TLS state via the request
	// context. We read it through the standard interface that
	// fiber exposes; nil means plaintext.
	req := c.RequestCtx()
	if req == nil {
		return nil
	}

	return req.TLSConnectionState()
}

// ErrInvalidPolicy wraps a policy validation failure. Loaders return
// this so callers can distinguish "config is wrong" from "filesystem
// is wrong" (which surfaces as os.PathError) or "secrets backend
// failed" (caller's concern).
var ErrInvalidPolicy = ewrap.New("httpauth: invalid policy")
