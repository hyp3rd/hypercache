package tests

import (
	"context"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/internal/cluster"
	"github.com/hyp3rd/hypercache/pkg/backend"
)

// TestDistMemoryHeartbeatLiveness spins up three nodes with a fast heartbeat interval
// and validates suspect -> removal transitions plus success/failure metrics.
func TestDistMemoryHeartbeatLiveness(t *testing.T) { //nolint:paralleltest
	// Intervals chosen so the test tolerates the 3-5x slowdown imposed by
	// the race detector. Previous values (interval=30ms, dead=120ms) were
	// tight enough that a delayed heartbeat tick could push *alive* nodes
	// past deadAfter under -race, removing them from membership.
	interval := 80 * time.Millisecond
	suspectAfter := 4 * interval // 320ms
	deadAfter := 8 * interval    // 640ms

	ring := cluster.NewRing(cluster.WithReplication(1))
	membership := cluster.NewMembership(ring)
	transport := backend.NewInProcessTransport()

	// nodes
	n1 := cluster.NewNode("", "n1:0")
	n2 := cluster.NewNode("", "n2:0")
	n3 := cluster.NewNode("", "n3:0")

	// backend for node1 with heartbeat enabled
	b1i, err := backend.NewDistMemory(
		context.TODO(),
		backend.WithDistMembership(membership, n1),
		backend.WithDistTransport(transport),
		backend.WithDistHeartbeat(interval, suspectAfter, deadAfter),
	)
	if err != nil {
		t.Fatalf("b1: %v", err)
	}

	b1, ok := b1i.(*backend.DistMemory)
	if !ok {
		t.Fatalf("failed to cast b1i to *backend.DistMemory")
	}

	StopOnCleanup(t, b1)

	// add peers (without heartbeat loops themselves)
	b2i, err := backend.NewDistMemory(
		context.TODO(),
		backend.WithDistMembership(membership, n2),
		backend.WithDistTransport(transport),
	)
	if err != nil {
		t.Fatalf("b2: %v", err)
	}

	b2, ok := b2i.(*backend.DistMemory)
	if !ok {
		t.Fatalf("failed to cast b2i to *backend.DistMemory")
	}

	StopOnCleanup(t, b2)

	b3i, err := backend.NewDistMemory(
		context.TODO(),
		backend.WithDistMembership(membership, n3),
		backend.WithDistTransport(transport),
	)
	if err != nil {
		t.Fatalf("b3: %v", err)
	}

	b3, ok := b3i.(*backend.DistMemory)
	if !ok {
		t.Fatalf("failed to cast b3i to *backend.DistMemory")
	}

	StopOnCleanup(t, b3)

	transport.Register(b1)
	transport.Register(b2)
	transport.Register(b3)

	// Wait until heartbeat marks peers alive (initial success probes)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		aliveCount := 0
		for _, n := range membership.List() {
			if n.State == cluster.NodeAlive {
				aliveCount++
			}
		}

		if aliveCount == 3 {
			break
		}

		time.Sleep(20 * time.Millisecond)
	}

	// Simulate node2 becoming unresponsive by removing it from transport registry.
	// (Simplest way: do not respond to health; drop entry.)
	transport.Unregister(string(n2.ID))

	// Wait until node2 transitions to suspect then removed.
	var sawSuspect bool

	deadline = time.Now().Add(2 * deadAfter)
	for time.Now().Before(deadline) {
		foundN2 := false
		for _, n := range membership.List() {
			if n.ID == n2.ID {
				foundN2 = true

				if n.State == cluster.NodeSuspect {
					sawSuspect = true
				}
			}
		}

		if !foundN2 && sawSuspect {
			break
		} // removed after suspicion observed

		time.Sleep(20 * time.Millisecond)
	}

	if !sawSuspect {
		t.Fatalf("node2 never became suspect")
	}

	// ensure removed
	for _, n := range membership.List() {
		if n.ID == n2.ID {
			t.Fatalf("node2 still present, state=%s", n.State)
		}
	}

	// Node3 should remain alive; ensure not removed
	n3Present := false
	for _, n := range membership.List() {
		if n.ID == n3.ID {
			n3Present = true

			if n.State != cluster.NodeAlive {
				t.Fatalf("node3 not alive: %s", n.State)
			}
		}
	}

	if !n3Present {
		t.Fatalf("node3 missing")
	}

	// Metrics sanity: at least one heartbeat failure and success recorded.
	m := b1.Metrics()
	if m.HeartbeatFailure == 0 {
		t.Errorf("expected heartbeat failures > 0")
	}

	if m.HeartbeatSuccess == 0 {
		t.Errorf("expected heartbeat successes > 0")
	}

	if m.NodesRemoved == 0 {
		t.Errorf("expected nodes removed metric > 0")
	}
}
