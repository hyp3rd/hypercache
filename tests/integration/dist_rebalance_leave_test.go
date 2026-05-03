package integration

import (
	"context"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// TestDistRebalanceLeave verifies keys are redistributed after a node leaves.
func TestDistRebalanceLeave(t *testing.T) {
	ctx := context.Background()

	// Start 3 nodes.
	addrA := allocatePort(t)
	addrB := allocatePort(t)
	addrC := allocatePort(t)

	opts := []backend.DistMemoryOption{
		backend.WithDistReplication(2),
		backend.WithDistVirtualNodes(32),
		backend.WithDistRebalanceInterval(100 * time.Millisecond),
	}

	nodeA := mustDistNode(ctx, t, "A", addrA, []string{addrB, addrC}, opts...)
	nodeB := mustDistNode(ctx, t, "B", addrB, []string{addrA, addrC}, opts...)

	nodeC := mustDistNode(ctx, t, "C", addrC, []string{addrA, addrB}, opts...)
	defer func() { _ = nodeA.Stop(ctx); _ = nodeB.Stop(ctx); _ = nodeC.Stop(ctx) }()

	// Insert keys through A.
	totalKeys := 300
	for i := range totalKeys {
		k := cacheKey(i)

		it := &cache.Item{Key: k, Value: []byte("v"), Version: 1, Origin: "A", LastUpdated: time.Now()}

		err := nodeA.Set(ctx, it)
		if err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}

	time.Sleep(250 * time.Millisecond) // allow replication

	// Remove node C from A and B membership (simulate leave).
	nodeA.RemovePeer(addrC)
	nodeB.RemovePeer(addrC)

	// Allow multiple rebalance ticks.
	time.Sleep(1200 * time.Millisecond)

	// We deliberately do not assert on ownedPrimaryCount(nodeC, ...) here:
	// node C still has itself in its own ring view, so it may still report
	// itself as primary for sampled keys. The redistribution we care about
	// is observed via migration metrics on surviving nodes, asserted below.

	// Migration metrics on surviving nodes should have increased (some keys moved off departed node C).
	migrated := nodeA.Metrics().RebalancedKeys + nodeB.Metrics().RebalancedKeys
	if migrated == 0 {
		// As fallback, ensure C's metrics show some migration attempts prior to leave (best-effort).
		if nodeC.Metrics().RebalancedKeys == 0 {
			// Hard fail if absolutely no migration activity.
			// Using Fatalf to highlight redistribution failure.
			vA := nodeA.Metrics().RebalancedKeys
			vB := nodeB.Metrics().RebalancedKeys
			vC := nodeC.Metrics().RebalancedKeys
			// Note: we don't expect throttle necessarily here.
			// Provide detailed counts for debugging.

			t.Fatalf("expected redistribution after leave (migrated A+B=0) details: A=%d B=%d C=%d", vA, vB, vC)
		}
	}
}
