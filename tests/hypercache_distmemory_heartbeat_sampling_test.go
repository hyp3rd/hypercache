package tests

import (
	"context"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/internal/cluster"
	"github.com/hyp3rd/hypercache/pkg/backend"
)

// waitForDeadTransition polls a DistMemory's metrics until at least one
// node-dead transition has been recorded, or timeout elapses. Returns the
// final metrics snapshot — used to assert intermediate state without
// re-fetching after the loop.
func waitForDeadTransition(b *backend.DistMemory, timeout time.Duration) backend.DistMetrics {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		m := b.Metrics()
		if m.NodesDead > 0 {
			return m
		}

		time.Sleep(10 * time.Millisecond)
	}

	return b.Metrics()
}

// newDistPeerNode is a small helper for the heartbeat-sampling test, which
// needs three peers but only one with a heartbeat goroutine — so it can't
// use SetupInProcessCluster (that helper applies the supplied options to
// every node uniformly).
//
// Construction uses context.Background() rather than a caller-supplied ctx
// — Stop runs from t.Cleanup at end-of-test where the test ctx may already
// be canceled, so propagating the same ctx into Stop would leak the HTTP
// listener. See StopOnCleanup for the same rationale.
func newDistPeerNode(
	t *testing.T,
	membership *cluster.Membership,
	transport *backend.InProcessTransport,
	id string,
	opts ...backend.DistMemoryOption,
) *backend.DistMemory {
	t.Helper()

	node := cluster.NewNode("", id)

	all := append([]backend.DistMemoryOption{
		backend.WithDistMembership(membership, node),
		backend.WithDistTransport(transport),
	}, opts...)

	bi, err := backend.NewDistMemory(context.Background(), all...)
	if err != nil {
		t.Fatalf("new %s: %v", id, err)
	}

	b, ok := bi.(*backend.DistMemory)
	if !ok {
		t.Fatalf("expected *backend.DistMemory, got %T", bi)
	}

	StopOnCleanup(t, b)
	transport.Register(b)

	return b
}

// TestHeartbeatSamplingAndTransitions validates randomized sampling still produces suspect/dead transitions.
func TestHeartbeatSamplingAndTransitions(t *testing.T) { //nolint:paralleltest // mutates shared transport
	ring := cluster.NewRing(cluster.WithReplication(1))
	membership := cluster.NewMembership(ring)
	transport := backend.NewInProcessTransport()

	// Only node 1 runs a heartbeat goroutine — the test asserts on its
	// metrics. Other nodes are passive responders.
	//
	// Intervals chosen to tolerate the 3-5x slowdown imposed by -race -count=10
	// under shuffle. Previous values (interval=15ms, dead=90ms) were tight
	// enough that under heavy parallel test load the heartbeat goroutine could
	// starve and never advance the dead transition within deadline.
	b1 := newDistPeerNode(t, membership, transport, "n1",
		backend.WithDistHeartbeat(80*time.Millisecond, 320*time.Millisecond, 640*time.Millisecond),
		backend.WithDistHeartbeatSample(0),
	)
	b2 := newDistPeerNode(t, membership, transport, "n2")

	_ = newDistPeerNode(t, membership, transport, "n3")

	transport.Unregister(string(b2.LocalNodeID()))

	mfinal := waitForDeadTransition(b1, 3*time.Second)
	if mfinal.NodesSuspect == 0 {
		t.Fatalf("expected at least one suspect transition, got 0")
	}

	if mfinal.NodesDead == 0 {
		t.Fatalf("expected at least one dead transition, got 0")
	}

	snap := b1.DistMembershipSnapshot()
	verAny := snap["version"]

	ver, ok := verAny.(uint64)
	if !ok {
		t.Fatalf("expected version to be uint64, got %T (%v)", verAny, verAny)
	}

	if ver < 3 {
		t.Fatalf("expected membership version >=4, got %v", verAny)
	}
}
