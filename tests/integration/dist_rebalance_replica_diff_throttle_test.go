package integration

import (
	"context"
	"testing"
	"time"

	backend "github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// TestDistRebalanceReplicaDiffThrottle ensures the per-tick limit increments throttle metric.
func TestDistRebalanceReplicaDiffThrottle(t *testing.T) {
	ctx := context.Background()

	addrA := allocatePort(t)
	addrB := allocatePort(t)

	// Low rebalance interval & strict replica diff limit of 1 per tick to force throttle.
	base := []backend.DistMemoryOption{
		backend.WithDistReplication(2),
		backend.WithDistVirtualNodes(16),
		backend.WithDistRebalanceInterval(80 * time.Millisecond),
		backend.WithDistReplicaDiffMaxPerTick(1),
	}

	nodeA := mustDistNode(t, ctx, "A", addrA, []string{addrB}, base...)

	nodeB := mustDistNode(t, ctx, "B", addrB, []string{addrA}, base...)
	defer func() { _ = nodeA.Stop(ctx); _ = nodeB.Stop(ctx) }()

	// Seed multiple keys.
	for i := range 25 {
		k := cacheKey(i)

		_ = nodeA.Set(ctx, &cache.Item{Key: k, Value: []byte("x"), Version: 1, Origin: "A", LastUpdated: time.Now()})
	}

	time.Sleep(250 * time.Millisecond)

	// Add third node with replication=3 so it becomes new replica for many keys.
	addrC := allocatePort(t)

	nodeC := mustDistNode(t, ctx, "C", addrC, []string{addrA, addrB}, append(base, backend.WithDistReplication(3))...)
	defer func() { _ = nodeC.Stop(ctx) }()

	nodeA.AddPeer(addrC)
	nodeB.AddPeer(addrC)

	// Allow several intervals so limit triggers.
	time.Sleep(900 * time.Millisecond)

	m := nodeA.Metrics()
	m2 := nodeB.Metrics()

	if (m.RebalanceReplicaDiffThrottle + m2.RebalanceReplicaDiffThrottle) == 0 {
		t.Fatalf("expected throttle metric to increment; A:%+v B:%+v", m, m2)
	}
}
