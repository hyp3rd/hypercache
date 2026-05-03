package tests

import (
	"context"
	"strconv"
	"testing"

	"github.com/hyp3rd/hypercache/internal/cluster"
	"github.com/hyp3rd/hypercache/pkg/backend"
)

// DistCluster bundles the in-process distributed-memory test fixture: a
// single shared Ring + Membership + Transport, and N DistMemory backends
// already wired into them. Most multi-node tests in this package follow the
// identical setup — extracting it as a helper keeps each test under the
// function-length budget and removes copy-paste drift.
type DistCluster struct {
	Ring       *cluster.Ring
	Membership *cluster.Membership
	Transport  *backend.InProcessTransport
	Nodes      []*backend.DistMemory
}

// ByID returns the node with the given ID, or nil if not present. Tests
// frequently need to look up a node by the ID returned from ring.Lookup —
// this avoids the manual `map[NodeID]*DistMemory{...}` lookup pattern that
// the dist_*_test.go files used to inline.
func (c *DistCluster) ByID(id cluster.NodeID) *backend.DistMemory {
	for _, n := range c.Nodes {
		if n.LocalNodeID() == id {
			return n
		}
	}

	return nil
}

// SetupInProcessCluster creates an N-node in-process distributed cluster
// with replication factor 3, plus any per-node options the caller supplies.
// Each backend is registered with the shared transport and scheduled for
// Stop on t.Cleanup.
//
// Node IDs follow the convention "n1", "n2", ... so tests can reason about
// them deterministically. The shared options are applied to every node;
// per-test customization (e.g. consistency, hint TTL, sample sizes) is
// expressed through the variadic option list.
// defaultReplicationFactor matches the cluster's recommended setting for
// quorum reads/writes — three replicas tolerates one failure while keeping
// total fan-out modest.
const defaultReplicationFactor = 3

// SetupInProcessCluster is shorthand for SetupInProcessClusterRF with the
// default replication factor.
func SetupInProcessCluster(tb testing.TB, n int, opts ...backend.DistMemoryOption) *DistCluster {
	tb.Helper()

	return SetupInProcessClusterRF(tb, n, defaultReplicationFactor, opts...)
}

// SetupInProcessClusterRF is the explicit-replication-factor variant of
// SetupInProcessCluster — used by tests that need 2-node clusters or
// non-default replica counts.
func SetupInProcessClusterRF(tb testing.TB, n, replication int, opts ...backend.DistMemoryOption) *DistCluster {
	tb.Helper()

	ring := cluster.NewRing(cluster.WithReplication(replication))
	membership := cluster.NewMembership(ring)
	transport := backend.NewInProcessTransport()

	dc := &DistCluster{
		Ring:       ring,
		Membership: membership,
		Transport:  transport,
		Nodes:      make([]*backend.DistMemory, 0, n),
	}

	for i := range n {
		node := cluster.NewNode("", "n"+strconv.Itoa(i+1)+":0")

		nodeOpts := append([]backend.DistMemoryOption{
			backend.WithDistMembership(membership, node),
			backend.WithDistTransport(transport),
		}, opts...)

		bi, err := backend.NewDistMemory(context.TODO(), nodeOpts...)
		if err != nil {
			tb.Fatalf("dist node %d: %v", i, err)
		}

		bk, ok := bi.(*backend.DistMemory)
		if !ok {
			tb.Fatalf("dist node %d: expected *backend.DistMemory, got %T", i, bi)
		}

		StopOnCleanup(tb, bk)
		transport.Register(bk)

		dc.Nodes = append(dc.Nodes, bk)
	}

	return dc
}
