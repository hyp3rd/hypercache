package tests

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/internal/sentinel"
	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// newDrainNode builds a single-node DistMemory with the HTTP transport
// running so /health and /dist/drain are reachable. Replication=1 keeps
// the test focused on the drain-state machine — fan-out concerns are
// covered elsewhere.
func newDrainNode(t *testing.T) *backend.DistMemory {
	t.Helper()

	addr := AllocatePort(t)

	bi, err := backend.NewDistMemory(
		context.Background(),
		backend.WithDistNode("drain-A", addr),
		backend.WithDistReplication(1),
	)
	if err != nil {
		t.Fatalf("new dist memory: %v", err)
	}

	dm, ok := bi.(*backend.DistMemory)
	if !ok {
		t.Fatalf("expected *backend.DistMemory, got %T", bi)
	}

	StopOnCleanup(t, dm)

	return dm
}

// httpStatus issues a request and returns its status code; bodies are
// drained so the connection returns to the pool. Test-helper-only —
// production code never ignores response bodies.
func httpStatus(ctx context.Context, t *testing.T, method, url string) int {
	t.Helper()

	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		t.Fatalf("build request %s %s: %v", method, url, err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request %s %s: %v", method, url, err)
	}

	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode
}

// TestDistDrain_HealthFlipsTo503 is the core C.1 contract: after Drain
// is called, the auth-wrapped /health endpoint reports 503 so external
// load balancers stop routing traffic. Pre-drain it must report 200,
// post-drain it must report 503; the transition is one-way.
func TestDistDrain_HealthFlipsTo503(t *testing.T) {
	t.Parallel()

	dm := newDrainNode(t)
	ctx := context.Background()
	healthURL := "http://" + dm.LocalNodeAddr() + "/health"

	if got := httpStatus(ctx, t, http.MethodGet, healthURL); got != http.StatusOK {
		t.Fatalf("expected /health=200 before drain, got %d", got)
	}

	err := dm.Drain(ctx)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}

	if got := httpStatus(ctx, t, http.MethodGet, healthURL); got != http.StatusServiceUnavailable {
		t.Fatalf("expected /health=503 after drain, got %d", got)
	}

	// Drain is idempotent.
	err2 := dm.Drain(ctx)
	if err2 != nil {
		t.Fatalf("second drain returned error: %v", err2)
	}

	// Metric increments exactly once per first-transition (CompareAndSwap).
	if got := dm.Metrics().Drains; got != 1 {
		t.Fatalf("expected Drains=1 after one transition + idempotent re-drain, got %d", got)
	}

	if !dm.IsDraining() {
		t.Fatal("IsDraining() must report true after Drain")
	}
}

// TestDistDrain_RejectsNewWrites is the corollary contract: while the
// node is draining, Set and Remove return ErrDraining so callers that
// haven't yet noticed the /health 503 still get a clear refusal
// instead of silently writing to a node that's about to disappear.
// Reads continue to succeed because the node still holds data.
func TestDistDrain_RejectsNewWrites(t *testing.T) {
	t.Parallel()

	dm := newDrainNode(t)
	ctx := context.Background()

	// Pre-drain Set must succeed so the post-drain Get has something to find.
	preErr := dm.Set(ctx, &cache.Item{
		Key:         "drain-key",
		Value:       []byte("v"),
		Version:     1,
		Origin:      "drain-A",
		LastUpdated: time.Now(),
	})
	if preErr != nil {
		t.Fatalf("pre-drain set: %v", preErr)
	}

	drainErr := dm.Drain(ctx)
	if drainErr != nil {
		t.Fatalf("drain: %v", drainErr)
	}

	postSetErr := dm.Set(ctx, &cache.Item{
		Key:         "post-drain-key",
		Value:       []byte("v"),
		Version:     1,
		Origin:      "drain-A",
		LastUpdated: time.Now(),
	})
	if !errors.Is(postSetErr, sentinel.ErrDraining) {
		t.Fatalf("expected ErrDraining on post-drain Set, got %v", postSetErr)
	}

	postRemoveErr := dm.Remove(ctx, "drain-key")
	if !errors.Is(postRemoveErr, sentinel.ErrDraining) {
		t.Fatalf("expected ErrDraining on post-drain Remove, got %v", postRemoveErr)
	}

	// Reads still work — operators expect drain to stop new writes,
	// not to abandon in-flight reads.
	it, ok := dm.Get(ctx, "drain-key")
	if !ok || it == nil {
		t.Fatal("expected Get on drain-key to still succeed during drain")
	}
}

// TestDistDrain_HTTPEndpointDrains exercises the operator path: a
// POST to /dist/drain transitions the node, /health flips to 503,
// and the metric increments. Mirrors the in-process test above but
// drives the transition through the wire — confirming the endpoint
// is actually wired and auth-wrapped consistent with the rest of
// /dist/* / /internal/*.
func TestDistDrain_HTTPEndpointDrains(t *testing.T) {
	t.Parallel()

	dm := newDrainNode(t)
	ctx := context.Background()

	base := "http://" + dm.LocalNodeAddr()

	if got := httpStatus(ctx, t, http.MethodGet, base+"/health"); got != http.StatusOK {
		t.Fatalf("expected /health=200 before drain, got %d", got)
	}

	if got := httpStatus(ctx, t, http.MethodPost, base+"/dist/drain"); got != http.StatusOK {
		t.Fatalf("expected /dist/drain=200, got %d", got)
	}

	if got := httpStatus(ctx, t, http.MethodGet, base+"/health"); got != http.StatusServiceUnavailable {
		t.Fatalf("expected /health=503 after HTTP drain, got %d", got)
	}

	if !dm.IsDraining() {
		t.Fatal("IsDraining() must report true after HTTP drain")
	}
}
