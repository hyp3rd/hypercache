//go:build test

package tests

import (
	"context"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/internal/cluster"
	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// generateReplicaWrites issues a few primary writes to force replication
// fan-out — used by failure-recovery tests to seed the hint queue once the
// replica has been disconnected.
func generateReplicaWrites(ctx context.Context, b *backend.DistMemory, key string, n int, gap time.Duration) {
	for range n {
		_ = b.Set(ctx, &cache.Item{Key: key, Value: "v1-update"})

		time.Sleep(gap)
	}
}

// waitForHintDelivery polls until the replica node returns a non-nil item
// for key, indicating the hinted-handoff replay has completed.
func waitForHintDelivery(ctx context.Context, replica *backend.DistMemory, key string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if it, ok := replica.Get(ctx, key); ok && it != nil {
			return true
		}

		time.Sleep(25 * time.Millisecond)
	}

	return false
}

// assertNodeFailureMetrics verifies the primary recorded suspect/dead
// transitions and hinted-queue activity after the replica went offline.
func assertNodeFailureMetrics(t *testing.T, m backend.DistMetrics) {
	t.Helper()

	if m.NodesSuspect == 0 {
		t.Fatalf("expected suspect transition recorded")
	}

	if m.NodesDead == 0 {
		t.Fatalf("expected dead transition recorded")
	}

	if m.HintedQueued == 0 {
		t.Fatalf("expected queued hints while replica unreachable")
	}
}

// TestDistFailureRecovery simulates node failure causing suspect->dead transition, hint queuing, and later recovery with hint replay.
func TestDistFailureRecovery(t *testing.T) { //nolint:paralleltest // mutates shared transport
	ctx := context.Background()

	dc := SetupInProcessClusterRF(
		t, 2, 2,
		backend.WithDistReplication(2),
		backend.WithDistHeartbeat(15*time.Millisecond, 40*time.Millisecond, 90*time.Millisecond),
		backend.WithDistHintTTL(2*time.Minute),
		backend.WithDistHintReplayInterval(20*time.Millisecond),
		backend.WithDistHintMaxPerNode(50),
	)

	b1, b2 := dc.Nodes[0], dc.Nodes[1]

	// Find a key where b1 is primary and b2 replica to ensure replication target.
	key, ok := FindOwnerKey(b1, "fail-key-", []cluster.NodeID{b1.LocalNodeID(), b2.LocalNodeID()}, 5000)
	if !ok {
		t.Fatalf("could not find deterministic key ordering")
	}

	_ = b1.Set(ctx, &cache.Item{Key: key, Value: "v1"})

	// Simulate b2 failure: unregister from transport so further replica writes queue hints.
	dc.Transport.Unregister(string(b2.LocalNodeID()))

	generateReplicaWrites(ctx, b1, key, 8, 5*time.Millisecond)
	waitForDeadTransition(b1, 2*time.Second)
	assertNodeFailureMetrics(t, b1.Metrics())

	// Bring b2 back online and force a replay cycle.
	dc.Transport.Register(b2)
	b1.ReplayHintsForTest(ctx)

	if !waitForHintDelivery(ctx, b2, key, 2*time.Second) {
		t.Fatalf("expected hinted value delivered after recovery")
	}

	if b1.Metrics().HintedReplayed == 0 {
		t.Fatalf("expected hinted replay metric >0")
	}
}
