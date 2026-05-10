package tests

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	fiber "github.com/gofiber/fiber/v3"
	"github.com/hyp3rd/ewrap"

	"github.com/hyp3rd/hypercache/internal/sentinel"
	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

const authTestToken = "s3cret-cluster-token"

// errCustomVerifyDenied is returned by the custom verify hook in
// TestDistHTTPAuth_CustomVerify when the request lacks the expected token.
var errCustomVerifyDenied = ewrap.New("custom verify denied")

// newAuthDistNode spins up a single DistMemory with HTTP auth enabled.
// The test waits for /health to respond before returning so subsequent
// auth assertions don't race against fiber's listener startup.
func newAuthDistNode(t *testing.T, auth backend.DistHTTPAuth) *backend.DistMemory {
	t.Helper()

	ctx := context.Background()
	addr := AllocatePort(t)

	bi, err := backend.NewDistMemory(
		ctx,
		backend.WithDistNode("auth-test", addr),
		backend.WithDistReplication(1),
		backend.WithDistHTTPAuth(auth),
	)
	if err != nil {
		t.Fatalf("new dist memory: %v", err)
	}

	dm, ok := bi.(*backend.DistMemory)
	if !ok {
		t.Fatalf("expected *backend.DistMemory, got %T", bi)
	}

	StopOnCleanup(t, dm)

	// /health is auth-wrapped; poll it with the right token so we know
	// the listener is up before the actual test asserts.
	if !waitForHealthAuthed(ctx, "http://"+dm.LocalNodeAddr(), auth.Token, 5*time.Second) {
		t.Fatal("dist HTTP server never came up")
	}

	return dm
}

// waitForHealthAuthed is a token-aware variant of waitForHealth. We need
// it because /health is now auth-wrapped when a token is configured.
func waitForHealthAuthed(ctx context.Context, baseURL, token string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/health", nil)
		if err != nil {
			return false
		}

		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				return true
			}
		}

		time.Sleep(50 * time.Millisecond)
	}

	return false
}

// TestDistHTTPAuth_RejectsUnauthenticatedRequest verifies the server
// returns 401 for requests with no Authorization header when a token is
// configured.
func TestDistHTTPAuth_RejectsUnauthenticatedRequest(t *testing.T) {
	t.Parallel()

	dm := newAuthDistNode(t, backend.DistHTTPAuth{Token: authTestToken})

	// Bypass the dist transport (which would auto-sign) and send a raw
	// request so we can prove the server enforces auth even when the
	// caller doesn't cooperate.
	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"http://"+dm.LocalNodeAddr()+"/internal/get?key=anything",
		nil,
	)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for missing token, got %d", resp.StatusCode)
	}
}

// TestDistHTTPAuth_RejectsWrongToken covers the case where the caller
// presents a token but it's the wrong one — must still 401, never leak
// any indication that the format was correct (constant-time compare
// guards against this side-channel).
func TestDistHTTPAuth_RejectsWrongToken(t *testing.T) {
	t.Parallel()

	dm := newAuthDistNode(t, backend.DistHTTPAuth{Token: authTestToken})

	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"http://"+dm.LocalNodeAddr()+"/internal/get?key=anything",
		nil,
	)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	req.Header.Set("Authorization", "Bearer not-the-right-token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong token, got %d", resp.StatusCode)
	}
}

// TestDistHTTPAuth_AcceptsValidToken sanity-checks that a request with
// the correct token is accepted. Without this companion test the
// 401-rejection tests above could pass even if the server rejected
// *every* request unconditionally.
func TestDistHTTPAuth_AcceptsValidToken(t *testing.T) {
	t.Parallel()

	dm := newAuthDistNode(t, backend.DistHTTPAuth{Token: authTestToken})

	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"http://"+dm.LocalNodeAddr()+"/health",
		nil,
	)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	req.Header.Set("Authorization", "Bearer "+authTestToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with valid token, got %d", resp.StatusCode)
	}
}

// newAuthReplicatedNode builds a 2-node-cluster member for the
// client-signing test. Extracted to a free function (rather than an
// in-test closure) so contextcheck doesn't follow the chain into
// StopOnCleanup's background-ctx cleanup. Construction uses
// context.Background() because Stop runs from t.Cleanup and a canceled
// outer ctx would leak the HTTP listener — same rationale as
// newHTTPMerkleNode in the merkle tests.
func newAuthReplicatedNode(t *testing.T, id, addr string, seeds []string, auth backend.DistHTTPAuth) *backend.DistMemory {
	t.Helper()

	bi, err := backend.NewDistMemory(
		context.Background(),
		backend.WithDistNode(id, addr),
		backend.WithDistSeeds(seeds),
		backend.WithDistReplication(2),
		backend.WithDistVirtualNodes(32),
		backend.WithDistHTTPAuth(auth),
	)
	if err != nil {
		t.Fatalf("new node %s: %v", id, err)
	}

	dm, ok := bi.(*backend.DistMemory)
	if !ok {
		t.Fatalf("cast %s: %T", id, bi)
	}

	StopOnCleanup(t, dm)

	return dm
}

// TestDistHTTPAuth_ClientSignsRequests verifies the auto-created HTTP
// client signs outgoing requests with the configured token. Built as a
// 2-node cluster: node A's primary writes get replicated to node B over
// the dist transport — if the client failed to sign, B would 401 the
// replication and B's local copy would never appear.
func TestDistHTTPAuth_ClientSignsRequests(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	addrA := AllocatePort(t)
	addrB := AllocatePort(t)

	auth := backend.DistHTTPAuth{Token: authTestToken}

	nodeA := newAuthReplicatedNode(t, "A", addrA, []string{addrB}, auth)
	nodeB := newAuthReplicatedNode(t, "B", addrB, []string{addrA}, auth)

	// Wait for both listeners — auth-aware health probe.
	for _, base := range []string{"http://" + nodeA.LocalNodeAddr(), "http://" + nodeB.LocalNodeAddr()} {
		if !waitForHealthAuthed(ctx, base, authTestToken, 5*time.Second) {
			t.Fatalf("node at %s never came up", base)
		}
	}

	// Allow ring/membership to settle.
	time.Sleep(200 * time.Millisecond)

	item := &cache.Item{
		Key:         "auth-prop-key",
		Value:       []byte("v1"),
		Version:     1,
		Origin:      "A",
		LastUpdated: time.Now(),
	}

	err := nodeA.Set(ctx, item)
	if err != nil {
		t.Fatalf("Set on nodeA: %v", err)
	}

	// The replicated value should appear on B within a few hundred ms;
	// if the client failed to sign, B would 401 the replication and the
	// poll would time out.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if it, ok := nodeB.Get(ctx, item.Key); ok && it != nil {
			return
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("replication did not propagate to nodeB — client likely failed to sign requests")
}

// errClientSignInvoked is the sentinel returned by the client-sign hook
// in TestDistHTTPAuth_RejectsClientSignOnlyConfig — a value the test
// can identify if the hook were ever invoked (it should not be: the
// constructor must reject before any HTTP traffic).
var errClientSignInvoked = ewrap.New("client sign hook should not run")

// TestDistHTTPAuth_RejectsClientSignOnlyConfig pins the
// constructor-time guard that prevents the silent-inbound-bypass shape
// (CVE-style: ClientSign set, no Token, no ServerVerify, no opt-in).
// Without this guard the dist HTTP server would have signed outbound
// traffic while accepting unauthenticated inbound — see
// sentinel.ErrInsecureAuthConfig for the rationale.
func TestDistHTTPAuth_RejectsClientSignOnlyConfig(t *testing.T) {
	t.Parallel()

	addr := AllocatePort(t)

	bi, err := backend.NewDistMemory(
		context.Background(),
		backend.WithDistNode("auth-reject", addr),
		backend.WithDistReplication(1),
		backend.WithDistHTTPAuth(backend.DistHTTPAuth{
			ClientSign: func(*http.Request) error { return errClientSignInvoked },
		}),
	)
	if !errors.Is(err, sentinel.ErrInsecureAuthConfig) {
		t.Fatalf("expected ErrInsecureAuthConfig, got err=%v bi=%v", err, bi)
	}

	if bi != nil {
		t.Fatalf("expected nil backend on validation failure, got %T", bi)
	}
}

// TestDistHTTPAuth_AnonymousInboundOptIn confirms operators can
// deliberately wire signed-out / open-in deployments by setting
// AllowAnonymousInbound — used when an L4 firewall or service mesh
// gates inbound at a layer below this server. The server must accept
// anonymous /internal/* requests while the auto-client still signs
// outbound.
func TestDistHTTPAuth_AnonymousInboundOptIn(t *testing.T) {
	t.Parallel()

	var signCalls atomic.Int64

	auth := backend.DistHTTPAuth{
		ClientSign: func(req *http.Request) error {
			signCalls.Add(1)
			req.Header.Set("X-Asymmetric-Sig", "ok")

			return nil
		},
		AllowAnonymousInbound: true,
	}

	dm := newAuthDistNode(t, auth)

	// Inbound /internal/get without any auth header must succeed
	// (returns 404 not-owner because no key is set, but importantly
	// not 401 — auth is skipped per the explicit opt-in).
	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"http://"+dm.LocalNodeAddr()+"/internal/get?key=anything",
		nil,
	)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("AllowAnonymousInbound did not skip auth wrap; got 401")
	}
}

// TestDistHTTPAuth_TokenWithClientSignOverride covers the asymmetric
// (but valid) shape where Token validates inbound and ClientSign
// overrides the default outbound header — e.g. a node fronting an HMAC
// peer mesh while still gating its own inbound on a shared bearer.
func TestDistHTTPAuth_TokenWithClientSignOverride(t *testing.T) {
	t.Parallel()

	var signCalls atomic.Int64

	auth := backend.DistHTTPAuth{
		Token: authTestToken,
		ClientSign: func(req *http.Request) error {
			signCalls.Add(1)
			req.Header.Set("Authorization", "Bearer "+authTestToken)

			return nil
		},
	}

	// Construction must succeed — Token covers inbound, ClientSign
	// overrides outbound, no insecure shape.
	dm := newAuthDistNode(t, auth)

	// Inbound without a token still 401s (Token-driven inbound).
	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"http://"+dm.LocalNodeAddr()+"/internal/get?key=anything",
		nil,
	)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("Token-inbound did not enforce; got %d", resp.StatusCode)
	}
}

// TestDistHTTPAuth_CustomVerify proves the ServerVerify escape hatch is
// invoked for every request and can deny on its own logic — used here
// to allow /health while requiring the bearer token elsewhere.
func TestDistHTTPAuth_CustomVerify(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64

	// Verifier: /health is public; everything else needs the token.
	verify := func(fctx fiber.Ctx) error {
		calls.Add(1)

		if fctx.Path() == "/health" {
			return nil
		}

		got := fctx.Get("Authorization")
		if got != "Bearer "+authTestToken {
			return errCustomVerifyDenied
		}

		return nil
	}

	dm := newAuthDistNode(t, backend.DistHTTPAuth{ServerVerify: verify})

	// /health works without any header (the custom verifier exempts it).
	if !waitForHealthAuthed(context.Background(), "http://"+dm.LocalNodeAddr(), "", 2*time.Second) {
		t.Fatal("public /health not reachable under custom verifier")
	}

	// /internal/get without token must 401.
	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"http://"+dm.LocalNodeAddr()+"/internal/get?key=x",
		nil,
	)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}

	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 from custom verifier without token, got %d", resp.StatusCode)
	}

	if calls.Load() == 0 {
		t.Fatal("custom verifier was never invoked")
	}
}
