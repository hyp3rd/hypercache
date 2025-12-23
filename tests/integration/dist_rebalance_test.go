package integration

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/internal/cluster"
	backend "github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// TestDistRebalanceJoin verifies keys are migrated to a new node after join.
func TestDistRebalanceJoin(t *testing.T) {
	ctx := context.Background()

	// Initial cluster: 2 nodes.
	addrA := allocatePort(t)
	addrB := allocatePort(t)

	nodeA := mustDistNode(
		t,
		ctx,
		"A",
		addrA,
		[]string{addrB},
		backend.WithDistReplication(2),
		backend.WithDistVirtualNodes(32),
		backend.WithDistRebalanceInterval(100*time.Millisecond),
	)

	nodeB := mustDistNode(
		t,
		ctx,
		"B",
		addrB,
		[]string{addrA},
		backend.WithDistReplication(2),
		backend.WithDistVirtualNodes(32),
		backend.WithDistRebalanceInterval(100*time.Millisecond),
	)
	defer func() { _ = nodeA.Stop(ctx); _ = nodeB.Stop(ctx) }()

	// Write a spread of keys via A.
	totalKeys := 300
	for i := range totalKeys {
		k := cacheKey(i)

		it := &cache.Item{Key: k, Value: []byte("v"), Version: 1, Origin: "A", LastUpdated: time.Now()}

		err := nodeA.Set(ctx, it)
		if err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}

	time.Sleep(200 * time.Millisecond) // allow initial replication

	// Capture ownership counts before join.
	skeys := sampleKeys(totalKeys)

	_ = ownedPrimaryCount(nodeA, skeys) // baseline (unused currently)
	_ = ownedPrimaryCount(nodeB, skeys)

	// Add third node C.
	addrC := allocatePort(t)

	nodeC := mustDistNode(
		t,
		ctx,
		"C",
		addrC,
		[]string{addrA, addrB},
		backend.WithDistReplication(2),
		backend.WithDistVirtualNodes(32),
		backend.WithDistRebalanceInterval(100*time.Millisecond),
	)
	defer func() { _ = nodeC.Stop(ctx) }()

	// Manually inject C into A and B membership (simulating gossip propagation delay that doesn't exist yet).
	nodeA.AddPeer(addrC)
	nodeB.AddPeer(addrC)

	// Allow membership to propagate + several rebalance ticks.
	time.Sleep(1200 * time.Millisecond)

	// Post-join ownership counts (sampled locally using isOwner logic via Get + Metrics ring lookup indirectly).
	postOwnedA := ownedPrimaryCount(nodeA, skeys)
	postOwnedB := ownedPrimaryCount(nodeB, skeys)
	postOwnedC := ownedPrimaryCount(nodeC, skeys)

	// Basic sanity: new node should now own > 0 keys.
	if postOwnedC == 0 {
		t.Fatalf("expected node C to own some keys after rebalancing")
	}

	// Distribution variance check: ensure no node has > 80% of sample (initial naive rebalance heuristic).
	maxAllowed := int(float64(totalKeys) * 0.80)
	if postOwnedA > maxAllowed || postOwnedB > maxAllowed || postOwnedC > maxAllowed {
		t.Fatalf("ownership still highly skewed: A=%d B=%d C=%d", postOwnedA, postOwnedB, postOwnedC)
	}

	// Rebalance metrics should show migrations (keys forwarded off old primaries) across cluster.
	migrated := nodeA.Metrics().RebalancedKeys + nodeB.Metrics().RebalancedKeys + nodeC.Metrics().RebalancedKeys
	if migrated == 0 {
		t.Fatalf("expected some rebalanced keys (total migrated=0)")
	}
}

// TestDistRebalanceThrottle simulates saturation causing throttle metric increments.
func TestDistRebalanceThrottle(t *testing.T) {
	ctx := context.Background()

	addrA := allocatePort(t)
	addrB := allocatePort(t)

	// Use small batch size & low concurrency to trigger throttling when many keys.
	opts := []backend.DistMemoryOption{
		backend.WithDistReplication(2),
		backend.WithDistVirtualNodes(16),
		backend.WithDistRebalanceInterval(50 * time.Millisecond),
		backend.WithDistRebalanceBatchSize(8),
		backend.WithDistRebalanceMaxConcurrent(1),
	}

	nodeA := mustDistNode(t, ctx, "A", addrA, []string{addrB}, opts...)

	nodeB := mustDistNode(t, ctx, "B", addrB, []string{addrA}, opts...)
	defer func() { _ = nodeA.Stop(ctx); _ = nodeB.Stop(ctx) }()

	// Populate many keys on A.
	for i := range 400 {
		k := cacheKey(i)

		it := &cache.Item{Key: k, Value: []byte("v"), Version: 1, Origin: "A", LastUpdated: time.Now()}

		err := nodeA.Set(ctx, it)
		if err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}

	// Add third node to force migrations while concurrency=1, which should queue batches.
	addrC := allocatePort(t)

	nodeC := mustDistNode(t, ctx, "C", addrC, []string{addrA, addrB}, opts...)
	defer func() { _ = nodeC.Stop(ctx) }()

	// propagate membership like in join test
	nodeA.AddPeer(addrC)
	nodeB.AddPeer(addrC)

	time.Sleep(1500 * time.Millisecond)

	// Expect throttle metric > 0 on some node (A likely source).
	if a, b, c := nodeA.Metrics().RebalanceThrottle, nodeB.Metrics().RebalanceThrottle, nodeC.Metrics().RebalanceThrottle; a == 0 &&
		b == 0 &&
		c == 0 {
		t.Fatalf("expected throttle metric to increment (a=%d b=%d c=%d)", a, b, c)
	}
}

// Helpers.

func mustDistNode(
	t *testing.T,
	ctx context.Context,
	id, addr string,
	seeds []string,
	extra ...backend.DistMemoryOption,
) *backend.DistMemory {
	opts := []backend.DistMemoryOption{
		backend.WithDistNode(id, addr),
		backend.WithDistSeeds(seeds),
		backend.WithDistHintReplayInterval(200 * time.Millisecond),
		backend.WithDistHintTTL(5 * time.Second),
		backend.WithDistReadConsistency(backend.ConsistencyQuorum),
		backend.WithDistWriteConsistency(backend.ConsistencyQuorum),
	}

	opts = append(opts, extra...)

	bm, err := backend.NewDistMemory(ctx, opts...)
	if err != nil {
		t.Fatalf("new dist memory: %v", err)
	}

	return bm.(*backend.DistMemory)
}

func cacheKey(i int) string { return "k" + strconv.Itoa(i) }

func sampleKeys(n int) []string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, cacheKey(i))
	}

	return out
}

func ownedPrimaryCount(dm *backend.DistMemory, keys []string) int {
	if dm == nil || dm.Membership() == nil {
		return 0
	}

	ring := dm.Membership().Ring()
	if ring == nil {
		return 0
	}

	c := 0

	self := cluster.NodeID(dm.LocalNodeID())
	for _, k := range keys {
		owners := ring.Lookup(k)
		if len(owners) > 0 && owners[0] == self {
			c++
		}
	}

	return c
}
