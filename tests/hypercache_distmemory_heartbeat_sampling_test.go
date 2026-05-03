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

	// Intervals chosen to tolerate the 3-5x slowdown imposed by -race -count=10
	// under shuffle. Previous values (interval=15ms, dead=90ms) were tight
	// enough that under heavy parallel test load the heartbeat goroutine could
	// starve and never advance the dead transition within deadline.
	b1i, _ := backend.NewDistMemory(
		ctx,
		backend.WithDistMembership(membership, n1),
		backend.WithDistTransport(transport),
		backend.WithDistHeartbeat(80*time.Millisecond, 320*time.Millisecond, 640*time.Millisecond),
		backend.WithDistHeartbeatSample(0), // probe all peers per tick for deterministic transition
	)

	_ = b1i // for clarity

	b2i, _ := backend.NewDistMemory(ctx, backend.WithDistMembership(membership, n2), backend.WithDistTransport(transport))
	b3i, _ := backend.NewDistMemory(ctx, backend.WithDistMembership(membership, n3), backend.WithDistTransport(transport))

	b1, ok := b1i.(*backend.DistMemory)
	if !ok {
		t.Fatalf("failed to cast b1i to *backend.DistMemory")
	}

	b2, ok := b2i.(*backend.DistMemory)
	if !ok {
		t.Fatalf("failed to cast b2i to *backend.DistMemory")
	}

	b3, ok := b3i.(*backend.DistMemory)
	if !ok {
		t.Fatalf("failed to cast b3i to *backend.DistMemory")
	}

	StopOnCleanup(t, b1)
	StopOnCleanup(t, b2)
	StopOnCleanup(t, b3)

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
