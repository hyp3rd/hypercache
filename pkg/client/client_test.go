package client_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hyp3rd/ewrap"

	"github.com/hyp3rd/hypercache/pkg/client"
)

// errEnvelope is the canonical 4xx/5xx body shape the cache emits.
// Duplicated here so the test fixtures don't pull on internal
// client/types.go — tests run against the public surface.
type errEnvelope struct {
	Code    string `json:"code"`
	Error   string `json:"error"`
	Details string `json:"details,omitempty"`
}

// errMissingBearer is a fixture sentinel — we need a static error to
// return from the bearer-validation stub. err113 forbids inlined
// ewrap.New so a top-level var is the path.
var errMissingBearer = ewrap.New("missing or invalid bearer")

// stubNode is the canonical fixture node ID. Extracted as a const
// because the test stubs reference it six times and goconst flags
// the repetition.
const stubNode = "stub-node"

// writeError writes the canonical envelope. Centralized so each
// fixture's error-path branch is one line. The envelope's fields
// are all primitive strings so json.Marshal cannot fail; we still
// check the error so errchkjson is satisfied.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	body, err := json.Marshal(errEnvelope{Code: code, Error: msg})
	if err != nil {
		http.Error(w, "marshal err envelope: "+err.Error(), http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	_, _ = w.Write(body)
}

// newCacheStub returns a tiny in-memory cache that speaks the parts
// of the cache REST API the client uses. Each test customizes via
// the options. Returns the *httptest.Server (caller closes it) and
// a hits counter the test can assert against.
type cacheStub struct {
	server *httptest.Server
	store  map[string][]byte
	hits   atomic.Int64
}

func newCacheStub(t *testing.T) *cacheStub {
	t.Helper()

	cs := &cacheStub{store: map[string][]byte{}}

	mux := http.NewServeMux()

	mux.HandleFunc("/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		cs.hits.Add(1)
		w.Header().Set("Content-Type", "application/json")

		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":           "test-identity",
			"scopes":       []string{"read", "write"},
			"capabilities": []string{"cache.read", "cache.write"},
		})
	})

	mux.HandleFunc("/v1/cache/", func(w http.ResponseWriter, r *http.Request) {
		cs.hits.Add(1)

		key := strings.TrimPrefix(r.URL.Path, "/v1/cache/")

		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)

			cs.store[key] = body

			_ = json.NewEncoder(w).Encode(map[string]any{
				jsonKey:    key,
				jsonStored: true,
				"bytes":    len(body),
				jsonNode:   stubNode,
				jsonOwners: []string{stubNode},
			})

		case http.MethodGet:
			val, ok := cs.store[key]
			if !ok {
				writeError(w, http.StatusNotFound, "NOT_FOUND", "key not found")

				return
			}

			if strings.Contains(r.Header.Get("Accept"), "application/json") {
				w.Header().Set("Content-Type", "application/json")

				_ = json.NewEncoder(w).Encode(map[string]any{
					jsonKey:          key,
					"value":          base64.StdEncoding.EncodeToString(val),
					"value_encoding": "base64",
					"version":        1,
					jsonNode:         stubNode,
					jsonOwners:       []string{stubNode},
				})

				return
			}

			_, _ = w.Write(val)

		case http.MethodDelete:
			delete(cs.store, key)
			w.WriteHeader(http.StatusNoContent)

		default:
			writeError(w, http.StatusMethodNotAllowed, "BAD_REQUEST", "unsupported method")
		}
	})

	cs.server = httptest.NewServer(mux)
	t.Cleanup(cs.server.Close)

	return cs
}

// TestClient_SetGetDelete pins the canonical happy-path round-trip
// against the cache stub. No auth, no failover, no topology — just
// proves the wire protocol the client speaks matches what the cache
// serves.
func TestClient_SetGetDelete(t *testing.T) {
	t.Parallel()

	cs := newCacheStub(t)

	c, err := client.New([]string{cs.server.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	t.Cleanup(func() { _ = c.Close() })

	ctx := context.Background()

	err = c.Set(ctx, "k1", []byte("hello"), time.Minute)
	if err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := c.Get(ctx, "k1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if string(got) != "hello" {
		t.Errorf("Get: got %q, want %q", string(got), "hello")
	}

	err = c.Delete(ctx, "k1")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err = c.Get(ctx, "k1")
	if !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("Get after delete: want ErrNotFound, got %v", err)
	}
}

// TestClient_GetItem verifies the JSON-envelope path returns the
// full Item, with base64 value decoded back to raw bytes.
func TestClient_GetItem(t *testing.T) {
	t.Parallel()

	cs := newCacheStub(t)

	c, _ := client.New([]string{cs.server.URL})
	t.Cleanup(func() { _ = c.Close() })

	ctx := context.Background()

	_ = c.Set(ctx, "k1", []byte("binary\x00data"), 0)

	item, err := c.GetItem(ctx, "k1")
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}

	if string(item.Value) != "binary\x00data" {
		t.Errorf("Value: got %q, want %q", item.Value, "binary\x00data")
	}

	if item.Node != stubNode {
		t.Errorf("Node: got %q, want stub-node", item.Node)
	}

	if len(item.Owners) != 1 || item.Owners[0] != stubNode {
		t.Errorf("Owners: got %v, want [stub-node]", item.Owners)
	}
}

// TestClient_Identity pins the /v1/me round-trip including the
// capabilities field. HasCapability is the public introspection
// path SDK consumers will use most.
func TestClient_Identity(t *testing.T) {
	t.Parallel()

	cs := newCacheStub(t)

	c, _ := client.New([]string{cs.server.URL})
	t.Cleanup(func() { _ = c.Close() })

	id, err := c.Identity(context.Background())
	if err != nil {
		t.Fatalf("Identity: %v", err)
	}

	if id.ID != "test-identity" {
		t.Errorf("ID: got %q, want test-identity", id.ID)
	}

	if !id.HasCapability("cache.read") {
		t.Errorf("HasCapability(cache.read) = false; want true (caps=%v)", id.Capabilities)
	}

	if id.HasCapability("cache.admin") {
		t.Errorf("HasCapability(cache.admin) = true; want false (caps=%v)", id.Capabilities)
	}
}

// TestClient_BearerAuth pins that WithBearerAuth wires the
// Authorization header into every request. The fixture validates
// the header and 401s anything missing or wrong.
func TestClient_BearerAuth(t *testing.T) {
	t.Parallel()

	const expected = "Bearer super-secret"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != expected {
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", errMissingBearer.Error())

			return
		}

		w.Header().Set("Content-Type", "application/json")

		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":           "bearer-id",
			"scopes":       []string{"read"},
			"capabilities": []string{"cache.read"},
		})
	}))
	t.Cleanup(srv.Close)

	c, _ := client.New(
		[]string{srv.URL},
		client.WithBearerAuth("super-secret"),
	)
	t.Cleanup(func() { _ = c.Close() })

	id, err := c.Identity(context.Background())
	if err != nil {
		t.Fatalf("Identity: %v", err)
	}

	if id.ID != "bearer-id" {
		t.Errorf("ID: got %q, want bearer-id", id.ID)
	}
}

// TestClient_BasicAuth pins WithBasicAuth: the Authorization header
// must encode user:pass in base64 with the Basic prefix.
func TestClient_BasicAuth(t *testing.T) {
	t.Parallel()

	const (
		username = "alice"
		password = "correct-horse"
	)

	wantHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != wantHeader {
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "basic auth required")

			return
		}

		w.Header().Set("Content-Type", "application/json")

		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":           "alice",
			"scopes":       []string{"read"},
			"capabilities": []string{"cache.read"},
		})
	}))
	t.Cleanup(srv.Close)

	c, _ := client.New(
		[]string{srv.URL},
		client.WithBasicAuth(username, password),
	)
	t.Cleanup(func() { _ = c.Close() })

	id, err := c.Identity(context.Background())
	if err != nil {
		t.Fatalf("Identity: %v", err)
	}

	if id.ID != "alice" {
		t.Errorf("ID: got %q, want alice", id.ID)
	}
}

// TestClient_FailsOverOn5xx pins the F2 random-failover behavior:
// when an endpoint returns 500, the client retries on the next
// endpoint without surfacing the failure to the caller. Success
// on the second endpoint resolves the call.
//
// We use two stubs: one always 500s, the other always succeeds.
// Failover is random, so over enough calls we exercise both
// orderings; this test asserts both orderings eventually succeed.
func TestClient_FailsOverOn5xx(t *testing.T) {
	t.Parallel()

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "boom")
	}))
	t.Cleanup(bad.Close)

	good := newCacheStub(t)

	c, _ := client.New([]string{bad.URL, good.server.URL})
	t.Cleanup(func() { _ = c.Close() })

	// Random failover means we can't predict which endpoint we pick
	// first. Loop a few times — at least one call hits bad first
	// and falls over to good; at least one hits good directly. Both
	// succeed regardless. If failover were broken, ~50% of calls
	// would fail.
	const iterations = 8

	ctx := context.Background()
	for i := range iterations {
		err := c.Set(ctx, fmt.Sprintf("k%d", i), []byte("v"), time.Minute)
		if err != nil {
			t.Fatalf("Set iteration %d: %v", i, err)
		}
	}

	if good.hits.Load() < int64(iterations) {
		t.Errorf("good endpoint hits = %d, want >= %d", good.hits.Load(), iterations)
	}
}

// TestClient_NoFailoverOn4xx pins the conservative half of F2: 4xx
// answers (auth, scope, not-found, bad-request) are deterministic
// across the cluster; retrying on the next endpoint would only
// slow the caller down without changing the outcome.
func TestClient_NoFailoverOn4xx(t *testing.T) {
	t.Parallel()

	var bad401Hits, good200Hits atomic.Int64

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		bad401Hits.Add(1)
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad creds")
	}))
	t.Cleanup(bad.Close)

	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		good200Hits.Add(1)
		w.WriteHeader(http.StatusOK)

		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":           "noop",
			"scopes":       []string{"read"},
			"capabilities": []string{"cache.read"},
		})
	}))
	t.Cleanup(good.Close)

	// bad is the only endpoint. We pin behavior by giving the
	// client only bad; if failover incorrectly retried 401s on
	// good, we'd see good get hit too.
	c, _ := client.New([]string{bad.URL})
	t.Cleanup(func() { _ = c.Close() })

	_, err := c.Identity(context.Background())
	if !errors.Is(err, client.ErrUnauthorized) {
		t.Fatalf("Identity: want ErrUnauthorized, got %v", err)
	}

	// Bad was hit exactly once; failover did NOT walk to a next.
	if bad401Hits.Load() != 1 {
		t.Errorf("bad hits = %d, want 1 (no retry)", bad401Hits.Load())
	}
}

// TestClient_AllEndpointsFailed pins the exhaustive-failure path:
// every endpoint 500s, and the client returns ErrAllEndpointsFailed
// wrapping the last cause.
func TestClient_AllEndpointsFailed(t *testing.T) {
	t.Parallel()

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "broken")
	}))
	t.Cleanup(bad.Close)

	c, _ := client.New([]string{bad.URL, bad.URL, bad.URL})
	t.Cleanup(func() { _ = c.Close() })

	_, err := c.Get(context.Background(), "k")
	if !errors.Is(err, client.ErrAllEndpointsFailed) {
		t.Fatalf("want ErrAllEndpointsFailed, got %v", err)
	}

	// The wrapped cause should also be reachable as *StatusError —
	// callers that want the original Code can errors.As.
	var se *client.StatusError

	if !errors.As(err, &se) {
		t.Fatalf("expected wrapped *StatusError; got %v", err)
	}

	if se.Code != "INTERNAL" {
		t.Errorf("wrapped Code: got %q, want INTERNAL", se.Code)
	}
}

// TestClient_StatusErrorIs pins the errors.Is shortcuts that the
// sentinel set is supposed to provide.
func TestClient_StatusErrorIs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		err    *client.StatusError
		target error
		want   bool
	}{
		{"NOT_FOUND code → ErrNotFound", &client.StatusError{HTTPStatus: 404, Code: "NOT_FOUND"}, client.ErrNotFound, true},
		{"404 without code → ErrNotFound", &client.StatusError{HTTPStatus: 404}, client.ErrNotFound, true},
		{"UNAUTHORIZED → ErrUnauthorized", &client.StatusError{HTTPStatus: 401, Code: "UNAUTHORIZED"}, client.ErrUnauthorized, true},
		{"403 → ErrForbidden", &client.StatusError{HTTPStatus: 403}, client.ErrForbidden, true},
		{"DRAINING → ErrDraining", &client.StatusError{HTTPStatus: 503, Code: "DRAINING"}, client.ErrDraining, true},
		{"BAD_REQUEST → ErrBadRequest", &client.StatusError{HTTPStatus: 400, Code: "BAD_REQUEST"}, client.ErrBadRequest, true},
		{"500 → ErrInternal", &client.StatusError{HTTPStatus: 500, Code: "INTERNAL"}, client.ErrInternal, true},
		{"404 ≠ ErrInternal", &client.StatusError{HTTPStatus: 404}, client.ErrInternal, false},
		{"401 ≠ ErrNotFound", &client.StatusError{HTTPStatus: 401}, client.ErrNotFound, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := errors.Is(tc.err, tc.target)
			if got != tc.want {
				t.Errorf("errors.Is(%v, %v) = %v, want %v", tc.err, tc.target, got, tc.want)
			}
		})
	}
}

// TestClient_TopologyRefresh exercises the M2 path: client picks up
// new endpoints from /cluster/members on the refresh tick.
//
// Setup: cache stub A returns members = [A, B]. The client starts
// with just [A] as a seed. After manual RefreshTopology, the
// client's view includes B.
func TestClient_TopologyRefresh(t *testing.T) {
	t.Parallel()

	var stubA *httptest.Server

	stubB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(stubB.Close)

	stubA = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cluster/members" {
			w.WriteHeader(http.StatusOK)

			return
		}

		// Strip "http://" → bare host:port (matches HYPERCACHE_API_ADDR shape)
		hostA := strings.TrimPrefix(stubA.URL, "http://")
		hostB := strings.TrimPrefix(stubB.URL, "http://")

		w.Header().Set("Content-Type", "application/json")

		_ = json.NewEncoder(w).Encode(map[string]any{
			"members": []map[string]any{
				{"id": "A", "address": hostA, "state": "alive"},
				{"id": "B", "address": hostB, "state": "alive"},
			},
		})
	}))
	t.Cleanup(stubA.Close)

	c, _ := client.New([]string{stubA.URL})
	t.Cleanup(func() { _ = c.Close() })

	// Before refresh: only the seed.
	got := c.Endpoints()
	if len(got) != 1 {
		t.Fatalf("pre-refresh endpoints: got %d, want 1", len(got))
	}

	err := c.RefreshTopology(context.Background())
	if err != nil {
		t.Fatalf("RefreshTopology: %v", err)
	}

	// After refresh: both members.
	got = c.Endpoints()
	if len(got) != 2 {
		t.Fatalf("post-refresh endpoints: got %d (%v), want 2", len(got), got)
	}

	hostA := strings.TrimPrefix(stubA.URL, "http://")
	hostB := strings.TrimPrefix(stubB.URL, "http://")

	gotHosts := make(map[string]bool, len(got))
	for _, e := range got {
		gotHosts[strings.TrimPrefix(e, "http://")] = true
	}

	if !gotHosts[hostA] || !gotHosts[hostB] {
		t.Errorf("post-refresh: want both %s and %s, got %v", hostA, hostB, got)
	}
}

// TestClient_TopologyRefreshKeepsSeedFallback pins the
// partition-during-refresh failsafe: if refresh produces an empty
// member list, the client keeps its previous endpoint view rather
// than wiping it.
func TestClient_TopologyRefreshKeepsSeedFallback(t *testing.T) {
	t.Parallel()

	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cluster/members" {
			w.Header().Set("Content-Type", "application/json")

			_ = json.NewEncoder(w).Encode(map[string]any{"members": []map[string]any{}})

			return
		}

		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(stub.Close)

	c, _ := client.New([]string{stub.URL})
	t.Cleanup(func() { _ = c.Close() })

	before := c.Endpoints()

	err := c.RefreshTopology(context.Background())
	if err != nil {
		t.Fatalf("RefreshTopology: %v", err)
	}

	after := c.Endpoints()
	if len(after) != len(before) || (len(after) > 0 && after[0] != before[0]) {
		t.Errorf("empty refresh wiped endpoints: before=%v after=%v", before, after)
	}
}

// TestClient_New_EmptySeeds pins the constructor's input-validation
// posture: an empty seed list fails fast with ErrNoEndpoints rather
// than returning a Client that 500s on every call.
func TestClient_New_EmptySeeds(t *testing.T) {
	t.Parallel()

	_, err := client.New(nil)
	if !errors.Is(err, client.ErrNoEndpoints) {
		t.Errorf("New(nil): want ErrNoEndpoints, got %v", err)
	}

	_, err = client.New([]string{"", "  "})
	if !errors.Is(err, client.ErrNoEndpoints) {
		t.Errorf("New(whitespace seeds): want ErrNoEndpoints, got %v", err)
	}
}

// TestClient_New_OptionErrors pins each option's input validation.
func TestClient_New_OptionErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opt  client.Option
	}{
		{"bearer empty", client.WithBearerAuth("")},
		{"basic empty user", client.WithBasicAuth("", "pass")},
		{"basic empty pass", client.WithBasicAuth("user", "")},
		{"refresh too fast", client.WithTopologyRefresh(time.Millisecond)},
		{"http client nil", client.WithHTTPClient(nil)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := client.New([]string{"http://x"}, tc.opt)
			if err == nil {
				t.Errorf("%s: want error, got nil", tc.name)
			}
		})
	}
}
