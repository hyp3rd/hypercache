package tests

import (
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/internal/cluster"
	"github.com/hyp3rd/hypercache/pkg/backend"
)

// waitForAllAlive polls the membership view until every node reports alive
// or the deadline expires. Used by heartbeat-liveness tests to ensure the
// initial probe round has completed before the test starts perturbing the
// cluster.
func waitForAllAlive(membership *cluster.Membership, expected int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		alive := 0

		for _, n := range membership.List() {
			if n.State == cluster.NodeAlive {
				alive++
			}
		}

		if alive == expected {
			return true
		}

		time.Sleep(20 * time.Millisecond)
	}

	return false
}

// nodeRemovalResult captures the outcome of waitForNodeRemoval — using a
// struct instead of two bool returns avoids the revive `confusing-results`
// lint while keeping the call site readable at the expense of one more
// type definition.
type nodeRemovalResult struct {
	SawSuspect bool
	Removed    bool
}

// waitForNodeRemoval polls until the named node has been removed from
// membership, having first transitioned through the Suspect state.
func waitForNodeRemoval(membership *cluster.Membership, target cluster.NodeID, timeout time.Duration) nodeRemovalResult {
	var result nodeRemovalResult

	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		found := false

		for _, n := range membership.List() {
			if n.ID == target {
				found = true

				if n.State == cluster.NodeSuspect {
					result.SawSuspect = true
				}
			}
		}

		if !found && result.SawSuspect {
			result.Removed = true

			return result
		}

		time.Sleep(20 * time.Millisecond)
	}

	return result
}

// waitForNodesRemovedMetric polls until b's NodesRemoved metric is non-zero
// or timeout elapses. The heartbeat goroutine increments this metric in
// evaluateLiveness *after* membership.Remove has succeeded — there is a
// tiny preemption window (worse under -race) where membership shows the
// node gone but the atomic.Add has not yet executed. Tests that observe
// the membership change first must wait for the metric to settle.
func waitForNodesRemovedMetric(b *backend.DistMemory, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if b.Metrics().NodesRemoved > 0 {
			return true
		}

		time.Sleep(10 * time.Millisecond)
	}

	return false
}

// assertHeartbeatLiveness verifies the post-condition of a heartbeat removal
// scenario: removed node is gone from membership, alive node is still
// present and alive, and the heartbeat metrics reflect the activity.
func assertHeartbeatLiveness(t *testing.T, membership *cluster.Membership, removed, stillAlive cluster.NodeID, m backend.DistMetrics) {
	t.Helper()

	for _, n := range membership.List() {
		if n.ID == removed {
			t.Fatalf("removed node %s still present, state=%s", removed, n.State)
		}
	}

	alivePresent := false

	for _, n := range membership.List() {
		if n.ID == stillAlive {
			alivePresent = true

			if n.State != cluster.NodeAlive {
				t.Fatalf("alive node %s not alive: %s", stillAlive, n.State)
			}
		}
	}

	if !alivePresent {
		t.Fatalf("alive node %s missing", stillAlive)
	}

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

// TestDistMemoryHeartbeatLiveness spins up three nodes with a fast heartbeat interval
// and validates suspect -> removal transitions plus success/failure metrics.
//
// Only node 1 runs a heartbeat goroutine — the test asserts on its metrics.
// Adding heartbeat to all three nodes makes the test flaky under -shuffle
// because node 2's own heartbeat goroutine (running while it's unregistered
// from the transport) interferes with timing of node 1's removal of node 2.
func TestDistMemoryHeartbeatLiveness(t *testing.T) { //nolint:paralleltest // mutates shared transport
	// Intervals chosen so the test tolerates the 3-5x slowdown imposed by
	// the race detector. Previous values (interval=30ms, dead=120ms) were
	// tight enough that a delayed heartbeat tick could push *alive* nodes
	// past deadAfter under -race, removing them from membership.
	const interval = 80 * time.Millisecond

	suspectAfter := 4 * interval // 320ms
	deadAfter := 8 * interval    // 640ms

	ring := cluster.NewRing(cluster.WithReplication(1))
	membership := cluster.NewMembership(ring)
	transport := backend.NewInProcessTransport()

	b1 := newDistPeerNode(t, membership, transport, "n1",
		backend.WithDistHeartbeat(interval, suspectAfter, deadAfter),
	)
	b2 := newDistPeerNode(t, membership, transport, "n2")
	b3 := newDistPeerNode(t, membership, transport, "n3")

	if !waitForAllAlive(membership, 3, 2*time.Second) {
		t.Fatalf("nodes never reached alive state within 2s")
	}

	target := b2.LocalNodeID()
	transport.Unregister(string(target))

	// 4*deadAfter (= 2560ms) gives the heartbeat goroutine slack under
	// -race -shuffle -count=10. Earlier 2*deadAfter (1280ms) was tight
	// enough that goroutine scheduling latency could miss the deadline
	// even when the probe logic was working correctly.
	res := waitForNodeRemoval(membership, target, 4*deadAfter)
	if !res.SawSuspect {
		t.Fatalf("node2 never became suspect")
	}

	if !res.Removed {
		t.Fatalf("node2 not removed within 2*deadAfter")
	}

	// Bridge the preemption window between membership.Remove() returning
	// and the heartbeat goroutine executing nodesRemoved.Add(1). Without
	// this poll the metric assertion below races and reports 0.
	if !waitForNodesRemovedMetric(b1, 500*time.Millisecond) {
		t.Fatalf("nodes-removed metric never reached >0 after membership transition")
	}

	assertHeartbeatLiveness(t, membership, target, b3.LocalNodeID(), b1.Metrics())
}
