package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fiber "github.com/gofiber/fiber/v3"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/pkg/backend"
	"github.com/hyp3rd/hypercache/pkg/httpauth"
)

// newAuthedServer mirrors newTestServer but drives registerClientRoutes
// directly so the auth middleware actually runs — handlers_test.go's
// newTestServer wires routes inline without the auth wrapper, which
// means regressions to the auth wiring would not be caught by that
// suite. Tests in this file own the auth contract instead.
//
// Replication=1 keeps assertions deterministic, the 127.0.0.1:0
// listener stays unbound (fiber.App.Test drives the wire in-memory),
// and the supplied policy shapes the auth wrapping.
func newAuthedServer(t *testing.T, policy httpauth.Policy) *fiber.App {
	t.Helper()

	cfg, err := hypercache.NewConfig[backend.DistMemory](constants.DistMemoryBackend)
	if err != nil {
		t.Fatalf("new config: %v", err)
	}

	cfg.DistMemoryOptions = []backend.DistMemoryOption{
		backend.WithDistNode("auth-test-node", "127.0.0.1:0"),
		backend.WithDistReplication(1),
	}

	hc, err := hypercache.New(t.Context(), hypercache.GetDefaultManager(), cfg)
	if err != nil {
		t.Fatalf("new hypercache: %v", err)
	}

	t.Cleanup(func() { _ = hc.Stop(context.Background()) })

	app := fiber.New()
	registerClientRoutes(app, policy, &nodeContext{hc: hc, nodeID: "auth-test-node"})

	return app
}

// singleTokenPolicy is the test-helper that turns a bare bearer
// string into a single-token Policy. Mirrors what
// httpauth.LoadFromEnv synthesizes for the legacy
// HYPERCACHE_AUTH_TOKEN path: one identity, all three scopes.
func singleTokenPolicy(token string) httpauth.Policy {
	return httpauth.Policy{
		Tokens: []httpauth.TokenIdentity{
			{
				ID:     "test",
				Token:  token,
				Scopes: []httpauth.Scope{httpauth.ScopeRead, httpauth.ScopeWrite, httpauth.ScopeAdmin},
			},
		},
	}
}

// openPolicy is the dev-mode policy: no credentials configured but
// AllowAnonymous flipped on, mirroring what main.go does when neither
// auth env var is set. Keeps the "open mode" test case identical to
// the binary's behavior.
func openPolicy() httpauth.Policy {
	return httpauth.Policy{AllowAnonymous: true}
}

// pathCacheKey is the canonical "/v1/cache/k" string reused across
// the test tables in this file. Hoisted to package scope so the
// goconst linter doesn't flag the repetition.
const pathCacheKey = "/v1/cache/k"

// authRequest issues a single request against the auth-wrapped app
// and returns just the status — auth assertions never need the body.
// Returning the bare int keeps the test bodies one line per case.
func authRequest(t *testing.T, app *fiber.App, method, target, authHeader string) int {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, target, strings.NewReader(""))
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test %s %s: %v", method, target, err)
	}

	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode
}

// TestBearerAuth_Required pins the wired-with-token contract. Each
// row is a single (header, expected status) tuple targeting a route
// that is supposed to be auth-protected — coverage here doubles as a
// regression test for `registerClientRoutes` forgetting to wrap a
// future route in bearerAuth.
func TestBearerAuth_Required(t *testing.T) {
	t.Parallel()

	const token = "s3cret-token"

	app := newAuthedServer(t, singleTokenPolicy(token))

	tests := []struct {
		name   string
		header string
		want   int
	}{
		{
			name:   "no header → 401",
			header: "",
			want:   http.StatusUnauthorized,
		},
		{
			name:   "wrong token → 401",
			header: "Bearer wrong-token",
			want:   http.StatusUnauthorized,
		},
		{
			name: "missing Bearer prefix → 401",
			// Same secret, but the canonical header form is
			// "Bearer <token>"; constant-time compare against the
			// pre-built `Bearer ...` byte sequence rejects this
			// without parsing the header.
			header: token,
			want:   http.StatusUnauthorized,
		},
		{
			name:   "lowercase scheme → 401",
			header: "bearer " + token,
			want:   http.StatusUnauthorized,
		},
		{
			name: "wrong-length token → 401",
			// Different length than the configured token —
			// constant-time compare on unequal-length inputs
			// returns 0 immediately. Catches a regression where
			// someone "optimizes" by short-circuiting on length
			// mismatch (which is exactly the timing leak the
			// fix is meant to defeat).
			header: "Bearer s",
			want:   http.StatusUnauthorized,
		},
		{
			name:   "correct token → 200",
			header: "Bearer " + token,
			want:   http.StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// /v1/owners/k is the cheapest auth-protected endpoint:
			// no body to read, no key to seed, no GetWithInfo
			// roundtrip. Any 401 here proves bearerAuth fired
			// before the handler.
			got := authRequest(t, app, http.MethodGet, "/v1/owners/k", tc.header)
			if got != tc.want {
				t.Fatalf("status: got %d, want %d", got, tc.want)
			}
		})
	}
}

// TestBearerAuth_OpenWhenTokenEmpty preserves the zero-config dev
// posture: when neither HYPERCACHE_AUTH_CONFIG nor
// HYPERCACHE_AUTH_TOKEN is set, main.go's run() flips
// AllowAnonymous on so every route stays reachable without
// credentials. The openPolicy() helper mirrors that exact shape.
func TestBearerAuth_OpenWhenTokenEmpty(t *testing.T) {
	t.Parallel()

	app := newAuthedServer(t, openPolicy())

	got := authRequest(t, app, http.MethodGet, "/v1/owners/k", "")
	if got != http.StatusOK {
		t.Fatalf("open mode: status %d, want 200", got)
	}
}

// TestBearerAuth_PublicRoutes guarantees that the auth-free meta
// endpoints — /healthz and /v1/openapi.yaml — stay reachable even
// when a token is configured. K8s liveness probes and the
// self-describing spec endpoint must never require credentials.
func TestBearerAuth_PublicRoutes(t *testing.T) {
	t.Parallel()

	app := newAuthedServer(t, singleTokenPolicy("s3cret-token"))

	publicRoutes := []string{"/healthz", "/v1/openapi.yaml"}
	for _, route := range publicRoutes {
		t.Run(route, func(t *testing.T) {
			t.Parallel()

			got := authRequest(t, app, http.MethodGet, route, "")
			if got != http.StatusOK {
				t.Fatalf("public route %s: status %d, want 200", route, got)
			}
		})
	}
}

// TestBearerAuth_AllProtectedRoutes asserts the auth wrapper fires
// for every cache and batch endpoint, not just /v1/owners. A bare
// route table here catches regressions where a new route gets added
// to registerClientRoutes without auth — the OpenAPI drift test
// covers documentation drift, this covers wiring drift.
func TestBearerAuth_AllProtectedRoutes(t *testing.T) {
	t.Parallel()

	app := newAuthedServer(t, singleTokenPolicy("s3cret-token"))

	protected := []struct {
		method string
		path   string
	}{
		{http.MethodPut, pathCacheKey},
		{http.MethodGet, pathCacheKey},
		{http.MethodHead, pathCacheKey},
		{http.MethodDelete, pathCacheKey},
		{http.MethodGet, "/v1/owners/k"},
		{http.MethodGet, "/v1/me"},
		{http.MethodPost, "/v1/cache/batch/get"},
		{http.MethodPost, "/v1/cache/batch/put"},
		{http.MethodPost, "/v1/cache/batch/delete"},
	}

	for _, route := range protected {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			t.Parallel()

			got := authRequest(t, app, route.method, route.path, "")
			if got != http.StatusUnauthorized {
				t.Fatalf("expected 401 without token, got %d", got)
			}
		})
	}
}

// TestScope_ReadOnlyToken pins the Phase 2 multi-token scope
// enforcement contract: a token granted only ScopeRead can hit
// GET routes but is forbidden from PUT/DELETE/batch-mutating
// routes. This catches the regression class where a future
// route is mistakenly tagged with the wrong scope in
// registerClientRoutes (e.g. a write-mutating endpoint accidentally
// wrapped in `read` middleware).
func TestScope_ReadOnlyToken(t *testing.T) {
	t.Parallel()

	policy := httpauth.Policy{
		Tokens: []httpauth.TokenIdentity{
			{ID: "ro", Token: "ro-token", Scopes: []httpauth.Scope{httpauth.ScopeRead}},
		},
	}

	app := newAuthedServer(t, policy)
	authHeader := "Bearer ro-token"

	cases := []struct {
		method string
		path   string
		want   int // 200 for read routes, 403 for write routes
	}{
		// Read scope: 200.
		{http.MethodGet, "/v1/owners/k", http.StatusOK},
		{http.MethodGet, "/v1/me", http.StatusOK},
		// Write scope: 403 (token has Read only).
		{http.MethodPut, pathCacheKey, http.StatusForbidden},
		{http.MethodDelete, pathCacheKey, http.StatusForbidden},
		{http.MethodPost, "/v1/cache/batch/put", http.StatusForbidden},
		{http.MethodPost, "/v1/cache/batch/delete", http.StatusForbidden},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			t.Parallel()

			got := authRequest(t, app, tc.method, tc.path, authHeader)
			if got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
}

// TestScope_MultipleIdentities exercises the multi-token resolution
// path: the policy carries two identities (ro + rw), and each is
// granted exactly the scopes its token presents. A read request
// with the rw token works (rw includes Read); a write request with
// the ro token does not (ro lacks Write).
func TestScope_MultipleIdentities(t *testing.T) {
	t.Parallel()

	policy := httpauth.Policy{
		Tokens: []httpauth.TokenIdentity{
			{ID: "ro", Token: "ro-token", Scopes: []httpauth.Scope{httpauth.ScopeRead}},
			{ID: "rw", Token: "rw-token", Scopes: []httpauth.Scope{httpauth.ScopeRead, httpauth.ScopeWrite}},
		},
	}

	app := newAuthedServer(t, policy)

	cases := []struct {
		name   string
		method string
		path   string
		token  string
		want   int
	}{
		{"ro reads", http.MethodGet, "/v1/owners/k", "ro-token", http.StatusOK},
		{"rw reads", http.MethodGet, "/v1/owners/k", "rw-token", http.StatusOK},
		{"ro writes (forbidden)", http.MethodPut, pathCacheKey, "ro-token", http.StatusForbidden},
		{"rw writes", http.MethodPut, pathCacheKey, "rw-token", http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := authRequest(t, app, tc.method, tc.path, "Bearer "+tc.token)
			if got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
}
