package tests

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-json"
	fiber "github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/pkg/backend"
	"github.com/hyp3rd/hypercache/pkg/httpauth"
)

// waitForManagementAddr polls hc.ManagementHTTPAddress until the listener
// has bound. Under -race the listener startup can take seconds, so a
// one-shot read would race.
func waitForManagementAddr(hc *hypercache.HyperCache[backend.InMemory], timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if addr := hc.ManagementHTTPAddress(); addr != "" {
			return addr
		}

		time.Sleep(10 * time.Millisecond)
	}

	return ""
}

// TestManagementHTTP_BasicEndpoints spins up the management HTTP server on an ephemeral port
// and validates core endpoints.
func TestManagementHTTP_BasicEndpoints(t *testing.T) {
	t.Parallel()

	cfg, err := hypercache.NewConfig[backend.InMemory](constants.InMemoryBackend)
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}

	cfg.HyperCacheOptions = append(
		cfg.HyperCacheOptions,
		hypercache.WithEvictionInterval[backend.InMemory](0),
		hypercache.WithManagementHTTP[backend.InMemory]("127.0.0.1:0"),
	)

	ctx := context.Background()
	hc, err := hypercache.New(ctx, hypercache.GetDefaultManager(), cfg)
	require.NoError(t, err)

	t.Cleanup(func() { _ = hc.Stop(ctx) })

	addr := waitForManagementAddr(hc, 5*time.Second)
	if addr == "" {
		t.Fatal("management HTTP listener did not bind within deadline")
	}

	client := &http.Client{Timeout: 5 * time.Second}

	get := func(path string) *http.Response {
		t.Helper()

		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+path, nil)
		require.NoError(t, reqErr)

		resp, doErr := client.Do(req)
		require.NoError(t, doErr)

		return resp
	}

	resp := get("/health")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	_ = resp.Body.Close()

	resp = get("/stats")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var statsBody map[string]any

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&statsBody))

	_ = resp.Body.Close()

	resp = get("/config")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var cfgBody map[string]any

	_ = json.NewDecoder(resp.Body).Decode(&cfgBody)
	_ = resp.Body.Close()

	require.NotEmpty(t, cfgBody)
	require.NotNil(t, cfgBody["evictionAlgorithm"])
}

// authScopingPolicy is the three-token policy used by
// TestManagementHTTP_AuthScoping. Hoisted to package scope so the
// test orchestrator stays under revive's function-length cap; the
// scope-coverage table is the same shape every time.
func authScopingPolicy() httpauth.Policy {
	return httpauth.Policy{
		Tokens: []httpauth.TokenIdentity{
			{ID: "ro", Token: "ro-token", Scopes: []httpauth.Scope{httpauth.ScopeRead}},
			{ID: "rw", Token: "rw-token", Scopes: []httpauth.Scope{httpauth.ScopeRead, httpauth.ScopeWrite}},
			{
				ID:     "admin",
				Token:  "admin-token",
				Scopes: []httpauth.Scope{httpauth.ScopeRead, httpauth.ScopeWrite, httpauth.ScopeAdmin},
			},
		},
	}
}

// startScopedMgmt builds an InMemory hypercache with the management
// HTTP server wired exactly the way `cmd/hypercache-server/main.go`
// wires it post-Phase-C2 — read-scope on the observability surface,
// admin-scope on the control surface — and returns the bound
// listener address. The hypercache is registered for cleanup on
// test exit.
func startScopedMgmt(t *testing.T, policy httpauth.Policy) string {
	t.Helper()

	cfg, err := hypercache.NewConfig[backend.InMemory](constants.InMemoryBackend)
	require.NoError(t, err)

	readAuth := func(c fiber.Ctx) error { return policy.Verify(c, httpauth.ScopeRead) }
	adminAuth := func(c fiber.Ctx) error { return policy.Verify(c, httpauth.ScopeAdmin) }

	cfg.HyperCacheOptions = append(
		cfg.HyperCacheOptions,
		hypercache.WithEvictionInterval[backend.InMemory](0),
		hypercache.WithManagementHTTP[backend.InMemory](
			"127.0.0.1:0",
			hypercache.WithMgmtAuth(readAuth),
			hypercache.WithMgmtControlAuth(adminAuth),
		),
	)

	hc, err := hypercache.New(t.Context(), hypercache.GetDefaultManager(), cfg)
	require.NoError(t, err)

	t.Cleanup(func() { _ = hc.Stop(context.Background()) })

	addr := waitForManagementAddr(hc, 5*time.Second)
	if addr == "" {
		t.Fatal("management HTTP listener did not bind within deadline")
	}

	return addr
}

// mgmtRequest issues a single request and returns the status code.
// Auth-scoping assertions only need the status; pulling this out
// keeps the test body row-oriented.
func mgmtRequest(t *testing.T, addr, method, path, token string) int {
	t.Helper()

	client := &http.Client{Timeout: 5 * time.Second}

	req, err := http.NewRequestWithContext(t.Context(), method, "http://"+addr+path, strings.NewReader(""))
	require.NoError(t, err)

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	require.NoError(t, err)

	_ = resp.Body.Close()

	return resp.StatusCode
}

// TestManagementHTTP_AuthScoping pins the Phase C2 mgmt-port auth
// posture wired by `cmd/hypercache-server/main.go`:
//
//   - /health is ALWAYS public (k8s liveness probes do not carry
//     credentials; making it auth-protected is a release-day
//     pod-restart-loop incident).
//   - /stats, /config, /cluster/*, /dist/* require read-or-better
//     scope (operators with a read token still see the dashboard).
//   - /evict, /clear, /trigger-expiration require admin scope
//     (the cluster-mutating control surface — ScopeWrite alone is
//     not enough).
//
// Drives the binary's exact wiring shape: WithMgmtAuth(readVerify)
// + WithMgmtControlAuth(adminVerify). A future regression that
// loses the per-route distinction (e.g., dropping
// WithMgmtControlAuth) would fail this test loudly.
func TestManagementHTTP_AuthScoping(t *testing.T) {
	t.Parallel()

	addr := startScopedMgmt(t, authScopingPolicy())

	t.Run("/health is public regardless of credentials", func(t *testing.T) {
		t.Parallel()

		require.Equal(t, http.StatusOK, mgmtRequest(t, addr, http.MethodGet, "/health", ""))
		require.Equal(t, http.StatusOK, mgmtRequest(t, addr, http.MethodGet, "/health", "ro-token"))
	})

	t.Run("read-scope routes accept any authed token", func(t *testing.T) {
		t.Parallel()

		for _, route := range []string{"/stats", "/config"} {
			require.Equal(t, http.StatusOK, mgmtRequest(t, addr, http.MethodGet, route, "ro-token"), route)
			require.Equal(t, http.StatusOK, mgmtRequest(t, addr, http.MethodGet, route, "rw-token"), route)
			require.Equal(t, http.StatusOK, mgmtRequest(t, addr, http.MethodGet, route, "admin-token"), route)
			require.Equal(t, http.StatusUnauthorized, mgmtRequest(t, addr, http.MethodGet, route, ""), route)
		}
	})

	t.Run("control routes require admin scope", func(t *testing.T) {
		t.Parallel()

		// Each row: (method, path, token, expected status). The
		// non-admin tokens must 403; admin must succeed (202 for
		// fire-and-forget, 200 for /clear).
		cases := []struct {
			method string
			path   string
			token  string
			want   int
		}{
			{http.MethodPost, "/evict", "", http.StatusUnauthorized},
			{http.MethodPost, "/evict", "ro-token", http.StatusForbidden},
			{http.MethodPost, "/evict", "rw-token", http.StatusForbidden},
			{http.MethodPost, "/evict", "admin-token", http.StatusAccepted},
			{http.MethodPost, "/trigger-expiration", "ro-token", http.StatusForbidden},
			{http.MethodPost, "/trigger-expiration", "admin-token", http.StatusAccepted},
			{http.MethodPost, "/clear", "rw-token", http.StatusForbidden},
			{http.MethodPost, "/clear", "admin-token", http.StatusOK},
		}

		for _, tc := range cases {
			got := mgmtRequest(t, addr, tc.method, tc.path, tc.token)
			require.Equalf(t, tc.want, got, "%s %s with token %q", tc.method, tc.path, tc.token)
		}
	})
}
