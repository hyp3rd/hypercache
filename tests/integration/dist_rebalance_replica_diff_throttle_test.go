package integration

import (
	"context"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/pkg/backend"
)

// TestDistRebalanceReplicaDiffThrottle ensures the per-tick limit increments throttle metric.
func TestDistRebalanceReplicaDiffThrottle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	addrA := allocatePort(t)
	addrB := allocatePort(t)

	// Low rebalance interval & strict replica diff limit of 1 per tick to force throttle.
	base := []backend.DistMemoryOption{ //nolint:prealloc // literal options; final size depends on test branches
		backend.WithDistReplication(2),
		backend.WithDistVirtualNodes(16),
		backend.WithDistRebalanceInterval(80 * time.Millisecond),
		backend.WithDistReplicaDiffMaxPerTick(1),
	}

	nodeA := mustDistNode(ctx, t, "A", addrA, []string{addrB}, base...)

	nodeB := mustDistNode(ctx, t, "B", addrB, []string{addrA}, base...)
	defer func() { _ = nodeA.Stop(ctx); _ = nodeB.Stop(ctx) }()

	// Seed keys on BOTH A and B so the post-join replica-diff has
	// candidates to push to C (without keys on B, the replica-diff
	// throttle would never fire because only A would have work).
	// DebugInject also fixes the prior shape's silently-swallowed
	// Set error.
	populateKeysOnAll(ctx, t, 25, nodeA, nodeB)

	time.Sleep(250 * time.Millisecond)

	// Add third node with replication=3 so it becomes new replica for many keys.
	addrC := allocatePort(t)

	nodeC := mustDistNode(ctx, t, "C", addrC, []string{addrA, addrB}, append(base, backend.WithDistReplication(3))...)
	defer func() { _ = nodeC.Stop(ctx) }()

	nodeA.AddPeer(addrC)
	nodeB.AddPeer(addrC)

	// Poll until the throttle metric increments rather than rely on a
	// hard sleep — under -race + shuffled test order the rebalancer's
	// 80ms-tick loop loses enough scheduling that 900ms of wall-clock
	// occasionally produces zero ticks. The 5s ceiling gives the
	// metric time to surface even on a contended runner; once it
	// surfaces we exit immediately, so the happy path stays fast.
	var (
		m, m2    backend.DistMetrics
		deadline = time.Now().Add(5 * time.Second)
	)

	for time.Now().Before(deadline) {
		m = nodeA.Metrics()
		m2 = nodeB.Metrics()

		if (m.RebalanceReplicaDiffThrottle + m2.RebalanceReplicaDiffThrottle) > 0 {
			return
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("expected throttle metric to increment within 5s; A:%+v B:%+v", m, m2)
}
