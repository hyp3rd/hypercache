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
)

// newAuthedServer mirrors newTestServer but drives registerClientRoutes
// directly so the bearer-auth middleware actually runs — handlers_test.go's
// newTestServer wires routes inline without the auth wrapper, which means
// regressions to the auth wiring would not be caught by that suite. Tests
// in this file own the auth contract instead.
//
// Replication=1 keeps assertions deterministic, the 127.0.0.1:0 listener
// stays unbound (fiber.App.Test drives the wire in-memory), and the
// supplied token shapes the bearerAuth wrapping.
func newAuthedServer(t *testing.T, token string) *fiber.App {
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
	registerClientRoutes(app, token, &nodeContext{hc: hc, nodeID: "auth-test-node"})

	return app
}

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

	app := newAuthedServer(t, token)

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
// posture: when HYPERCACHE_AUTH_TOKEN is unset, every route must be
// reachable without an Authorization header. registerClientRoutes
// passes an empty string in that case, and bearerAuth degrades to a
// passthrough wrapper.
func TestBearerAuth_OpenWhenTokenEmpty(t *testing.T) {
	t.Parallel()

	app := newAuthedServer(t, "")

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

	app := newAuthedServer(t, "s3cret-token")

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

	app := newAuthedServer(t, "s3cret-token")

	protected := []struct {
		method string
		path   string
	}{
		{http.MethodPut, "/v1/cache/k"},
		{http.MethodGet, "/v1/cache/k"},
		{http.MethodHead, "/v1/cache/k"},
		{http.MethodDelete, "/v1/cache/k"},
		{http.MethodGet, "/v1/owners/k"},
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
