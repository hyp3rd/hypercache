package integration

import (
	"context"
	"testing"
	"time"

	backend "github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// TestDistRebalanceReplicaDiff ensures that when a new replica is added (primary unchanged)
// the new replica eventually receives the keys via replica-only diff replication.
func TestDistRebalanceReplicaDiff(t *testing.T) {
	ctx := context.Background()

	// Start with two nodes replication=2 so both are owners for each key.
	addrA := allocatePort(t)
	addrB := allocatePort(t)

	baseOpts := []backend.DistMemoryOption{
		backend.WithDistReplication(2),
		backend.WithDistVirtualNodes(32),
		backend.WithDistRebalanceInterval(120 * time.Millisecond),
	}

	nodeA := mustDistNode(t, ctx, "A", addrA, []string{addrB}, baseOpts...)

	nodeB := mustDistNode(t, ctx, "B", addrB, []string{addrA}, baseOpts...)
	defer func() { _ = nodeA.Stop(ctx); _ = nodeB.Stop(ctx) }()

	// Insert a set of keys through primary (either node). We'll use A.
	totalKeys := 200
	for i := range totalKeys {
		k := cacheKey(i)

		it := &cache.Item{Key: k, Value: []byte("v"), Version: 1, Origin: "A", LastUpdated: time.Now()}
		err := nodeA.Set(ctx, it)
		if err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}

	time.Sleep(300 * time.Millisecond) // allow initial replication

	// Add third node C and increase replication factor logically by injecting membership only.
	// Since replication factor is fixed per process instance, we simulate a ring change where C participates
	// as a replica for some keys (virtual nodes distribution will produce owners including C) by simply adding the peer.
	addrC := allocatePort(t)

	nodeC := mustDistNode(t, ctx, "C", addrC, []string{addrA, addrB}, append(baseOpts, backend.WithDistReplication(3))...)
	defer func() { _ = nodeC.Stop(ctx) }()

	// Propagate C to existing nodes (they still have replication=2 configured, but ring will include C;
	// some keys will now have different replica sets where primary stays same but a new replica appears).
	nodeA.AddPeer(addrC)
	nodeB.AddPeer(addrC)

	// Allow several rebalance intervals for replica-only diff to trigger.
	time.Sleep(1500 * time.Millisecond)

	// Sample keys and ensure node C has received at least some of them (without being primary necessarily).
	present := 0
	for i := range totalKeys {
		k := cacheKey(i)
		if nodeC.LocalContains(k) { // presence implies replication happened (either primary migration or replica diff)
			present++
		}
	}

	if present == 0 {
		mC := nodeC.Metrics()
		mA := nodeA.Metrics()
		mB := nodeB.Metrics()
		// Provide diagnostic metrics for failure analysis.
		// Fail: expected replica diff replication to materialize some keys on C.
		t.Fatalf("expected node C to receive some replica-only diff keys (present=0) metrics A:%+v B:%+v C:%+v", mA, mB, mC)
	}
}
