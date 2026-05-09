package tests

import (
	"bufio"
	"context"
	"fmt"
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

// TestManagementHTTP_BearerAndOIDCCoexistence pins the Phase C
// hybrid posture: a Policy with BOTH static `Tokens` AND a
// `ServerVerify` hook (the OIDC verifier) authenticates either
// flow correctly. The resolve chain in pkg/httpauth/policy.go is:
//
//	bearer match against Tokens?  → yes, win
//	                              → no, fall through
//	mTLS cert?                    → not in this test
//	ServerVerify(c)?              → yes, win
//	                              → no, 401
//
// This test proves: (a) a static-bearer-formatted token wins via
// Tokens; (b) a token NOT in Tokens falls through to ServerVerify;
// (c) when ServerVerify rejects, the request 401s. ServerVerify is
// a hand-rolled stand-in for the production OIDC verifier — it
// recognizes the literal string "oidc-jwt-stand-in" as a valid
// "JWT" and grants admin scope. Real OIDC validation (signature,
// JWKS, claims) is covered by cmd/hypercache-server/oidc_test.go;
// this test covers only the resolve-chain wiring.
func TestManagementHTTP_BearerAndOIDCCoexistence(t *testing.T) {
	t.Parallel()

	const (
		staticToken    = "static-bearer-token"
		oidcStandInJWT = "oidc-jwt-stand-in"
	)

	policy := httpauth.Policy{
		Tokens: []httpauth.TokenIdentity{
			{
				ID:     "machine-integration",
				Token:  staticToken,
				Scopes: []httpauth.Scope{httpauth.ScopeRead, httpauth.ScopeWrite, httpauth.ScopeAdmin},
			},
		},
		ServerVerify: func(c fiber.Ctx) (httpauth.Identity, error) {
			authHeader := c.Get("Authorization")
			if authHeader != "Bearer "+oidcStandInJWT {
				return httpauth.Identity{}, fiber.ErrUnauthorized
			}

			return httpauth.Identity{
				ID:     "operator@example.com",
				Scopes: []httpauth.Scope{httpauth.ScopeRead, httpauth.ScopeWrite, httpauth.ScopeAdmin},
			}, nil
		},
	}

	addr := startScopedMgmt(t, policy)

	// /stats requires read-or-better; both flows should grant it.
	t.Run("static bearer authenticates via Tokens path", func(t *testing.T) {
		t.Parallel()

		got := mgmtRequest(t, addr, http.MethodGet, "/stats", staticToken)
		require.Equal(t, http.StatusOK, got)
	})

	t.Run("OIDC-shaped bearer falls through to ServerVerify", func(t *testing.T) {
		t.Parallel()

		got := mgmtRequest(t, addr, http.MethodGet, "/stats", oidcStandInJWT)
		require.Equal(t, http.StatusOK, got)
	})

	t.Run("unrecognized bearer 401s after both paths reject", func(t *testing.T) {
		t.Parallel()

		got := mgmtRequest(t, addr, http.MethodGet, "/stats", "neither-static-nor-oidc")
		require.Equal(t, http.StatusUnauthorized, got)
	})

	t.Run("/evict requires admin; both flows grant it", func(t *testing.T) {
		t.Parallel()

		require.Equal(t, http.StatusAccepted, mgmtRequest(t, addr, http.MethodPost, "/evict", staticToken))
		require.Equal(t, http.StatusAccepted, mgmtRequest(t, addr, http.MethodPost, "/evict", oidcStandInJWT))
	})
}

// TestManagementHTTP_ClusterEvents pins the SSE wire contract.
// Three properties matter to the monitor:
//
//  1. Connect with a valid token → 200 + Content-Type
//     `text/event-stream`. (The monitor's EventSource refuses to
//     reconnect without this exact MIME type.)
//  2. Initial frames carry a `members` snapshot and a `heartbeat`
//     snapshot — operators see the topology immediately, without
//     waiting for the next mutation.
//  3. Live publishes (the 1 Hz heartbeat ticker, in this test)
//     reach the subscriber and serialize as
//     `event: heartbeat\ndata: <json>\n\n`.
//
// Drives DistMemory because EventBus is wired there; InMemory's
// SSE handler 503s on the missing capability (covered by a
// separate sub-assertion below).
func TestManagementHTTP_ClusterEvents(t *testing.T) {
	t.Parallel()

	cfg, err := hypercache.NewConfig[backend.DistMemory](constants.DistMemoryBackend)
	require.NoError(t, err)

	cfg.HyperCacheOptions = append(
		cfg.HyperCacheOptions,
		hypercache.WithManagementHTTP[backend.DistMemory]("127.0.0.1:0"),
	)
	cfg.DistMemoryOptions = []backend.DistMemoryOption{
		backend.WithDistReplication(1),
		backend.WithDistVirtualNodes(32),
		backend.WithDistNode("test-node", "local"),
	}

	hc, err := hypercache.New(t.Context(), hypercache.GetDefaultManager(), cfg)
	require.NoError(t, err)

	t.Cleanup(func() { _ = hc.Stop(context.Background()) })

	baseURL := waitForMgmt(t, hc)

	t.Run("initial snapshot frames + live heartbeat tick", func(t *testing.T) {
		t.Parallel()

		// 8s deadline: the SSE stream MUST survive past the
		// historic 5 s WriteTimeout default (the bug Phase C
		// hardening fixed). A regression to a non-zero
		// WriteTimeout would force-close the connection at the
		// 5 s mark and the post-5 s frame read below would fail
		// with EOF / "other side closed".
		ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
		defer cancel()

		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/cluster/events", http.NoBody)
		require.NoError(t, reqErr)
		req.Header.Set("Accept", "text/event-stream")

		client := &http.Client{} // no timeout — SSE is long-lived

		resp, doErr := client.Do(req)
		require.NoError(t, doErr)

		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

		reader := bufio.NewReader(resp.Body)

		// Frame 1: members snapshot.
		frame, err := readSSEFrame(reader)
		require.NoError(t, err)
		require.Equal(t, "members", frame.Event)
		require.Contains(t, frame.Data, `"members"`)

		// Frame 2: heartbeat snapshot.
		frame, err = readSSEFrame(reader)
		require.NoError(t, err)
		require.Equal(t, "heartbeat", frame.Event)
		require.Contains(t, frame.Data, `"heartbeatSuccess"`)

		// Frame 3+: read 6 more heartbeat ticks. With the 1 Hz
		// heartbeat ticker, that's roughly 6 seconds of stream
		// time — well past the 5 s historic WriteTimeout. If
		// fasthttp ever reset its per-response write deadline
		// to a non-zero value here, the read would error around
		// the 5 s mark and this loop would surface it.
		for range 6 {
			frame, err = readSSEFrame(reader)
			require.NoError(t, err, "stream closed unexpectedly mid-iteration; check WriteTimeout regression")
			require.Equal(t, "heartbeat", frame.Event)
		}
	})
}

// sseFrame is the parsed shape of one Server-Sent Events frame.
// Returning a struct (rather than two same-typed strings) keeps
// callers from confusing argument order at the call site — same
// reason revive's `confusing-results` flags two-of-the-same-type
// returns.
type sseFrame struct {
	Event string
	Data  string
}

// readSSEFrame parses one SSE frame (event: TYPE\ndata: JSON\n\n
// shape) from r. Returns the parsed frame plus any read error
// wrapped with the failing read context. Lines like `retry:` are
// skipped — they don't terminate a frame.
func readSSEFrame(r *bufio.Reader) (sseFrame, error) {
	var frame sseFrame

	for {
		line, readErr := r.ReadString('\n')
		if readErr != nil {
			return frame, fmt.Errorf("read sse frame: %w", readErr)
		}

		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if frame.Event != "" || frame.Data != "" {
				return frame, nil
			}

			continue // skip blank lines until first non-empty
		}

		switch {
		case strings.HasPrefix(line, "event:"):
			frame.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			frame.Data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		default:
			// Ignore retry:/id:/comment lines — they're part of
			// SSE but not what we assert on.
		}
	}
}
