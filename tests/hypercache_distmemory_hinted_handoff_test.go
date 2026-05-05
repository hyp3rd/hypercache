//go:build test

package tests

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/internal/cluster"
	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// findKeyOwnedBy scans candidate keys whose ring owners include the named
// node. Returns the first match or `defaultKey` if no candidate qualifies
// within attempts.
func findKeyOwnedBy(node *backend.DistMemory, ownerID string, attempts int, defaultKey, prefix string) string {
	for i := range attempts {
		candidate := fmt.Sprintf("%s-%d", prefix, i)
		for _, oid := range node.Ring().Lookup(candidate) {
			if string(oid) == ownerID {
				return candidate
			}
		}
	}

	return defaultKey
}

// assertHintedHandoffMetrics fails the test unless the primary observed at
// least one queued + one replayed hint and zero dropped.
func assertHintedHandoffMetrics(t *testing.T, m backend.DistMetrics) {
	t.Helper()

	if m.HintedQueued < 1 {
		t.Fatalf("expected HintedQueued >=1, got %d", m.HintedQueued)
	}

	if m.HintedReplayed < 1 {
		t.Fatalf("expected HintedReplayed >=1, got %d", m.HintedReplayed)
	}

	if m.HintedDropped != 0 {
		t.Fatalf("expected no HintedDropped, got %d", m.HintedDropped)
	}
}

// newHintedHandoffNode constructs one DistMemory backend wired into the
// shared membership/transport for the hinted-handoff test. Construction
// uses context.Background() rather than a caller-supplied ctx because Stop
// runs from t.Cleanup at end-of-test where the test ctx may already be
// canceled — see StopOnCleanup for the same rationale.
func newHintedHandoffNode(t *testing.T, m *cluster.Membership, id string, baseOpts []backend.DistMemoryOption) *backend.DistMemory {
	t.Helper()

	opts := make([]backend.DistMemoryOption, 0, len(baseOpts)+2)

	opts = append(opts, baseOpts...)
	opts = append(
		opts,
		backend.WithDistNode(id, id),
		backend.WithDistMembership(m, cluster.NewNode(id, id)),
	)

	bi, err := backend.NewDistMemory(context.Background(), opts...)
	if err != nil {
		t.Fatalf("new %s: %v", id, err)
	}

	b, ok := bi.(*backend.DistMemory)
	if !ok {
		t.Fatalf("expected *backend.DistMemory, got %T", bi)
	}

	StopOnCleanup(t, b)

	return b
}

// TestHintedHandoffReplay ensures that when a replica is down during a write, a hint is queued and later replayed.
func TestHintedHandoffReplay(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	transport := backend.NewInProcessTransport()

	baseOpts := []backend.DistMemoryOption{
		backend.WithDistReplication(2),
		backend.WithDistWriteConsistency(backend.ConsistencyOne),
		backend.WithDistHintTTL(time.Minute),
		backend.WithDistHintReplayInterval(25 * time.Millisecond),
		backend.WithDistHintMaxPerNode(10),
	}

	ring := cluster.NewRing(cluster.WithReplication(2))
	membership := cluster.NewMembership(ring)
	membership.Upsert(cluster.NewNode("P", "P"))
	membership.Upsert(cluster.NewNode("R", "R"))

	p := newHintedHandoffNode(t, membership, "P", baseOpts)
	r := newHintedHandoffNode(t, membership, "R", baseOpts)

	p.SetTransport(transport)
	transport.Register(p)
	// r is deliberately not registered yet (simulate downed replica).

	// Constructor may have skipped replay due to timing — start it manually.
	p.StartHintReplayForTest(ctx)

	key := findKeyOwnedBy(p, "R", 100, "hint-key", "hint-key")

	_ = p.Set(ctx, &cache.Item{Key: key, Value: "v1"})

	if p.HintedQueueSize("R") == 0 {
		t.Fatalf("expected hint queued for unreachable replica; size=0 key=%s owners=%v", key, p.Ring().Lookup(key))
	}

	// Bring R online and force an immediate replay cycle.
	r.SetTransport(transport)
	transport.Register(r)
	p.ReplayHintsForTest(ctx)

	t.Logf("queued hints for R: %d", p.HintedQueueSize("R"))

	if !waitForHintDelivery(ctx, r, key, 1*time.Second) {
		t.Fatalf("replica did not receive hinted handoff value")
	}

	assertHintedHandoffMetrics(t, p.Metrics())
}
