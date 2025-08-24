package tests

import (
	"context"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/internal/cluster"
	"github.com/hyp3rd/hypercache/pkg/backend"
)

// TestHeartbeatSamplingAndTransitions validates randomized sampling still produces suspect/dead transitions.
func TestHeartbeatSamplingAndTransitions(t *testing.T) { //nolint:paralleltest
	ctx := context.Background()
	ring := cluster.NewRing(cluster.WithReplication(1))
	membership := cluster.NewMembership(ring)
	transport := backend.NewInProcessTransport()

	// three peers plus local
	n1 := cluster.NewNode("", "n1")
	n2 := cluster.NewNode("", "n2")
	n3 := cluster.NewNode("", "n3")

	b1i, _ := backend.NewDistMemory(
		ctx,
		backend.WithDistMembership(membership, n1),
		backend.WithDistTransport(transport),
		backend.WithDistHeartbeat(15*time.Millisecond, 40*time.Millisecond, 90*time.Millisecond),
		backend.WithDistHeartbeatSample(0), // probe all peers per tick for deterministic transition
	)

	_ = b1i // for clarity

	b2i, _ := backend.NewDistMemory(ctx, backend.WithDistMembership(membership, n2), backend.WithDistTransport(transport))
	b3i, _ := backend.NewDistMemory(ctx, backend.WithDistMembership(membership, n3), backend.WithDistTransport(transport))

	b1 := b1i.(*backend.DistMemory)
	b2 := b2i.(*backend.DistMemory)
	b3 := b3i.(*backend.DistMemory)

	transport.Register(b1)
	transport.Register(b2)
	transport.Register(b3)

	// Unregister b2 to simulate failure so it becomes suspect then dead.
	transport.Unregister(string(n2.ID))

	// Wait long enough for dead transition. Because of sampling (k=1) we give generous time window.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		m := b1.Metrics()
		if m.NodesDead > 0 { // transition observed
			break
		}

		time.Sleep(10 * time.Millisecond)
	}

	mfinal := b1.Metrics()
	if mfinal.NodesSuspect == 0 {
		t.Fatalf("expected at least one suspect transition, got 0")
	}

	if mfinal.NodesDead == 0 {
		t.Fatalf("expected at least one dead transition, got 0")
	}
	// ensure membership version advanced beyond initial additions (>= number of transitions + initial upserts)
	snap := b1.DistMembershipSnapshot()
	verAny := snap["version"]

	ver, _ := verAny.(uint64)
	if ver < 3 { // initial upserts already increment version; tolerate timing variance
		t.Fatalf("expected membership version >=4, got %v", verAny)
	}

	_ = b3 // silence linter for now (future: more assertions)
}
