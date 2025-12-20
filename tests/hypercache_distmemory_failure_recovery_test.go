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

// TestDistFailureRecovery simulates node failure causing suspect->dead transition, hint queuing, and later recovery with hint replay.
func TestDistFailureRecovery(t *testing.T) { //nolint:paralleltest
	ctx := context.Background()

	ring := cluster.NewRing(cluster.WithReplication(2))
	membership := cluster.NewMembership(ring)
	transport := backend.NewInProcessTransport()

	// two nodes (primary+replica) with fast heartbeat & hint config
	n1 := cluster.NewNode("", "n1")
	n2 := cluster.NewNode("", "n2")

	b1i, _ := backend.NewDistMemory(ctx,
		backend.WithDistMembership(membership, n1),
		backend.WithDistTransport(transport),
		backend.WithDistReplication(2),
		backend.WithDistHeartbeat(15*time.Millisecond, 40*time.Millisecond, 90*time.Millisecond),
		backend.WithDistHintTTL(2*time.Minute),
		backend.WithDistHintReplayInterval(20*time.Millisecond),
		backend.WithDistHintMaxPerNode(50),
	)
	b2i, _ := backend.NewDistMemory(ctx,
		backend.WithDistMembership(membership, n2),
		backend.WithDistTransport(transport),
		backend.WithDistReplication(2),
		backend.WithDistHeartbeat(15*time.Millisecond, 40*time.Millisecond, 90*time.Millisecond),
		backend.WithDistHintTTL(2*time.Minute),
		backend.WithDistHintReplayInterval(20*time.Millisecond),
		backend.WithDistHintMaxPerNode(50),
	)

	b1 := b1i.(*backend.DistMemory)
	b2 := b2i.(*backend.DistMemory)

	transport.Register(b1)
	transport.Register(b2)

	// Find a key where b1 is primary and b2 replica to ensure replication target
	key, ok := FindOwnerKey(b1, "fail-key-", []cluster.NodeID{b1.LocalNodeID(), b2.LocalNodeID()}, 5000)
	if !ok {
		t.Fatalf("could not find deterministic key ordering")
	}

	_ = b1.Set(ctx, &cache.Item{Key: key, Value: "v1"})

	// Simulate b2 failure (unregister from transport) so further replica writes queue hints.
	transport.Unregister(string(n2.ID))

	// Generate writes that should attempt to replicate and thus queue hints for n2.
	for range 8 { // a few writes to ensure some dropped into hints
		_ = b1.Set(ctx, &cache.Item{Key: key, Value: "v1-update"})

		time.Sleep(5 * time.Millisecond)
	}

	// Wait for suspect then dead transition of b2 from b1's perspective.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		m := b1.Metrics()
		if m.NodesDead > 0 { // dead transition observed
			break
		}

		time.Sleep(15 * time.Millisecond)
	}

	m1 := b1.Metrics()
	if m1.NodesSuspect == 0 {
		t.Fatalf("expected suspect transition recorded")
	}

	if m1.NodesDead == 0 {
		t.Fatalf("expected dead transition recorded")
	}

	if m1.HintedQueued == 0 {
		t.Fatalf("expected queued hints while replica unreachable")
	}

	// Bring b2 back (register again) and allow hint replay to run.
	transport.Register(b2)

	// Force a manual replay cycle then ensure loop running.
	b1.ReplayHintsForTest(ctx)

	// Wait for replay to deliver hints.
	deadline = time.Now().Add(2 * time.Second)

	delivered := false
	for time.Now().Before(deadline) {
		if it, ok := b2.Get(ctx, key); ok && it != nil {
			delivered = true

			break
		}

		time.Sleep(25 * time.Millisecond)
	}

	if !delivered {
		t.Fatalf("expected hinted value delivered after recovery")
	}

	// Ensure replay metrics advanced (at least one replay)
	m2 := b1.Metrics()
	if m2.HintedReplayed == 0 {
		t.Fatalf("expected hinted replay metric >0")
	}
}
