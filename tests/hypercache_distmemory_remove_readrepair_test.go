package tests

import (
	"context"
	"testing"

	"github.com/hyp3rd/hypercache/internal/cluster"
	"github.com/hyp3rd/hypercache/pkg/backend"
	v2 "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// helper to build two-node replicated cluster.
func newTwoNodeCluster(t *testing.T) (*backend.DistMemory, *backend.DistMemory, *cluster.Ring) { //nolint:thelper
	ring := cluster.NewRing(cluster.WithReplication(2))
	membership := cluster.NewMembership(ring)
	transport := backend.NewInProcessTransport()
	n1 := cluster.NewNode("", "node1:0")
	n2 := cluster.NewNode("", "node2:0")

	b1i, err := backend.NewDistMemory(context.TODO(), backend.WithDistMembership(membership, n1), backend.WithDistTransport(transport))
	if err != nil {
		t.Fatalf("b1: %v", err)
	}

	b2i, err := backend.NewDistMemory(context.TODO(), backend.WithDistMembership(membership, n2), backend.WithDistTransport(transport))
	if err != nil {
		t.Fatalf("b2: %v", err)
	}

	b1 := b1i.(*backend.DistMemory) //nolint:forcetypeassert
	b2 := b2i.(*backend.DistMemory) //nolint:forcetypeassert

	transport.Register(b1)
	transport.Register(b2)

	return b1, b2, ring
}

// TestDistMemoryRemoveReplication ensures that Remove replicates deletions across replicas.
func TestDistMemoryRemoveReplication(t *testing.T) {
	b1, b2, ring := newTwoNodeCluster(t)
	key := "remove-key"

	owners := ring.Lookup(key)
	if len(owners) == 0 {
		t.Fatalf("no owners")
	}

	item := &v2.Item{Key: key, Value: "val"}

	err := item.Valid()
	if err != nil {
		t.Fatalf("valid: %v", err)
	}
	// write via primary
	if owners[0] == b1.LocalNodeID() { // local helper we add below
		err := b1.Set(context.Background(), item)
		if err != nil {
			t.Fatalf("set: %v", err)
		}
	} else {
		err := b2.Set(context.Background(), item)
		if err != nil {
			t.Fatalf("set: %v", err)
		}
	}
	// assert item readable from both nodes
	if _, ok := b1.Get(context.Background(), key); !ok {
		t.Fatalf("b1 missing pre-remove")
	}

	if _, ok := b2.Get(context.Background(), key); !ok {
		t.Fatalf("b2 missing pre-remove")
	}
	// remove via primary
	if owners[0] == b1.LocalNodeID() {
		err := b1.Remove(context.Background(), key)
		if err != nil {
			t.Fatalf("remove: %v", err)
		}
	} else {
		err := b2.Remove(context.Background(), key)
		if err != nil {
			t.Fatalf("remove: %v", err)
		}
	}

	if _, ok := b1.Get(context.Background(), key); ok {
		t.Fatalf("b1 still has key after remove")
	}

	if _, ok := b2.Get(context.Background(), key); ok {
		t.Fatalf("b2 still has key after remove")
	}
}

// TestDistMemoryReadRepair ensures a stale replica is healed on read.
func TestDistMemoryReadRepair(t *testing.T) {
	b1, b2, ring := newTwoNodeCluster(t)
	key := "rr-key"

	owners := ring.Lookup(key)
	if len(owners) == 0 {
		t.Fatalf("no owners")
	}

	item := &v2.Item{Key: key, Value: "val"}

	err := item.Valid()
	if err != nil {
		t.Fatalf("valid: %v", err)
	}
	// write via primary
	if owners[0] == b1.LocalNodeID() {
		err := b1.Set(context.Background(), item)
		if err != nil {
			t.Fatalf("set: %v", err)
		}
	} else {
		err := b2.Set(context.Background(), item)
		if err != nil {
			t.Fatalf("set: %v", err)
		}
	}
	// determine replica node (owners[1]) and drop local copy there manually
	if len(owners) < 2 {
		t.Skip("replication factor <2")
	}

	replica := owners[1]
	// optional: t.Logf("owners: %v primary=%s replica=%s", owners, owners[0], replica)
	if replica == b1.LocalNodeID() {
		b1.DebugDropLocal(key)
	} else {
		b2.DebugDropLocal(key)
	}
	// ensure dropped locally
	if replica == b1.LocalNodeID() && b1.LocalContains(key) {
		t.Fatalf("replica still has key after drop")
	}

	if replica == b2.LocalNodeID() && b2.LocalContains(key) {
		t.Fatalf("replica still has key after drop")
	}
	// issue Get from a non-owner node to trigger forwarding, then verify owners repaired.
	// choose a requester: use node that is neither primary nor replica if possible; with 2 nodes this means primary forwards to replica or
	// vice versa.
	requester := b1
	if owners[0] == b1.LocalNodeID() && replica == b2.LocalNodeID() {
		requester = b2 // request from replica to forward to primary
	} else if owners[0] == b2.LocalNodeID() && replica == b1.LocalNodeID() {
		requester = b1
	}

	if _, ok := requester.Get(context.Background(), key); !ok {
		t.Fatalf("get for read-repair failed")
	}
	// after forwarding, both owners should have key locally again
	if owners[0] == b1.LocalNodeID() && !b1.LocalContains(key) {
		t.Fatalf("primary missing after read repair")
	}

	if owners[0] == b2.LocalNodeID() && !b2.LocalContains(key) {
		t.Fatalf("primary missing after read repair")
	}

	if replica == b1.LocalNodeID() && !b1.LocalContains(key) {
		t.Fatalf("replica missing after read repair")
	}

	if replica == b2.LocalNodeID() && !b2.LocalContains(key) {
		t.Fatalf("replica missing after read repair")
	}
	// metrics should show at least one read repair
	var repaired bool
	if replica == b1.LocalNodeID() {
		repaired = b1.Metrics().ReadRepair > 0
	} else {
		repaired = b2.Metrics().ReadRepair > 0
	}

	if !repaired {
		t.Fatalf("expected read-repair metric increment")
	}
}
