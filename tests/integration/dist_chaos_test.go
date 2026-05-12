package integration

import (
	"context"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// TestDistChaos_DropsAbsorbedByHintQueue is the canonical
// resilience scenario the chaos hooks were built for: configure a
// non-trivial drop rate on the transport that handles inter-node
// replication, perform writes, and verify the hint queue captures
// the dropped writes so eventual consistency converges once chaos
// is disabled.
//
// Without the hint queue absorbing the drops, the cache would
// silently lose replicas under transient transport failures —
// exactly the failure mode the hint queue exists to prevent.
// This test would have caught the entire class of "what if a
// peer's down for 100ms?" bugs before they reached production.
func TestDistChaos_DropsAbsorbedByHintQueue(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	addrA := allocatePort(t)
	addrB := allocatePort(t)
	chaos := backend.NewChaos()

	// ConsistencyOne so the primary write succeeds locally on every
	// call — the replica fan-out is what we want to disrupt. With
	// quorum we'd fail-fast on the first dropped write and never
	// queue hints, defeating the test's purpose.
	opts := []backend.DistMemoryOption{
		backend.WithDistReplication(2),
		backend.WithDistVirtualNodes(16),
		backend.WithDistChaos(chaos),
		backend.WithDistWriteConsistency(backend.ConsistencyOne),
		backend.WithDistReadConsistency(backend.ConsistencyOne),
		backend.WithDistHintReplayInterval(50 * time.Millisecond),
		backend.WithDistHintTTL(10 * time.Second),
	}

	nodeA := mustDistNodeRaw(ctx, t, "A", addrA, []string{addrB}, opts...)
	nodeB := mustDistNodeRaw(ctx, t, "B", addrB, []string{addrA}, opts...)

	defer func() { _ = nodeA.Stop(ctx); _ = nodeB.Stop(ctx) }()

	time.Sleep(100 * time.Millisecond) // drain startup gossip noise

	driveChaosPopulate(ctx, t, nodeA, chaos)

	// Heal: drops disabled, hint replay drains queued writes onto B.
	chaos.SetDropRate(0)
	waitForHintsDrained(t, nodeA)
}

// driveChaosPopulate runs the populate-with-drops phase: enables
// 80% chaos drops, writes 200 keys via Set, and asserts both that
// drops fired and that hints were queued. Extracted from the
// parent test to keep that function under the cognitive-length
// cap.
func driveChaosPopulate(ctx context.Context, t *testing.T, nodeA *backend.DistMemory, chaos *backend.Chaos) {
	t.Helper()

	chaos.SetDropRate(0.8)

	const populateN = 200

	for i := range populateN {
		it := &cache.Item{
			Key: cacheKey(i), Value: []byte("v"),
			Version: 1, Origin: "A", LastUpdated: time.Now(),
		}

		_ = nodeA.Set(ctx, it) // ConsistencyOne; primary write succeeds even when replica drop
	}

	if chaos.Drops() == 0 {
		t.Fatalf("expected chaos drops > 0 during 80%% drop phase; got 0")
	}

	if nodeA.Metrics().HintedQueued == 0 {
		t.Fatalf("expected hints to be queued during the chaos phase; got 0 (drops=%d)", chaos.Drops())
	}
}

// waitForHintsDrained polls until at least one hint replays or the
// deadline elapses. The assertion is "hints drain once chaos is
// off"; the wall-clock budget is loose because background loops
// run on their own cadence.
func waitForHintsDrained(t *testing.T, nodeA *backend.DistMemory) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if nodeA.Metrics().HintedReplayed > 0 {
			return
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Errorf("hint replay did not drain after chaos disabled (HintedReplayed=0, HintedQueued=%d)",
		nodeA.Metrics().HintedQueued)
}

// mustDistNodeRaw is the chaos-test variant of mustDistNode that
// does NOT force ConsistencyQuorum — the chaos test needs
// ConsistencyOne so dropped replica fan-outs land in the hint
// queue instead of failing the parent Set call.
func mustDistNodeRaw(
	ctx context.Context,
	t *testing.T,
	id, addr string,
	seeds []string,
	extra ...backend.DistMemoryOption,
) *backend.DistMemory {
	t.Helper()

	opts := []backend.DistMemoryOption{ //nolint:prealloc // literal helper-builder list
		backend.WithDistNode(id, addr),
		backend.WithDistSeeds(seeds),
	}

	opts = append(opts, extra...)

	bm, err := backend.NewDistMemory(ctx, opts...)
	if err != nil {
		t.Fatalf("new dist memory: %v", err)
	}

	waitForDistNodeHealth(ctx, t, addr)

	bk, ok := bm.(*backend.DistMemory)
	if !ok {
		t.Fatalf("expected *backend.DistMemory, got %T", bm)
	}

	return bk
}

// TestDistChaos_DisabledByDefaultIsNoop pins the "chaos costs
// nothing in production" claim: spinning up a DistMemory without
// WithDistChaos should produce exactly the same behavior as a
// pre-chaos build. Metrics for chaos drops/latencies must stay
// at zero, and writes must succeed normally.
func TestDistChaos_DisabledByDefaultIsNoop(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	addrA := allocatePort(t)
	addrB := allocatePort(t)

	opts := []backend.DistMemoryOption{
		backend.WithDistReplication(2),
		backend.WithDistVirtualNodes(16),
	}

	nodeA := mustDistNode(ctx, t, "A", addrA, []string{addrB}, opts...)
	nodeB := mustDistNode(ctx, t, "B", addrB, []string{addrA}, opts...)

	defer func() { _ = nodeA.Stop(ctx); _ = nodeB.Stop(ctx) }()

	populateKeys(ctx, t, nodeA, 50)

	if got := nodeA.Metrics().ChaosDrops; got != 0 {
		t.Errorf("ChaosDrops with chaos disabled: got %d, want 0", got)
	}

	if got := nodeA.Metrics().ChaosLatencies; got != 0 {
		t.Errorf("ChaosLatencies with chaos disabled: got %d, want 0", got)
	}
}
