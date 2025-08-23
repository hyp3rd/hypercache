package tests

import (
	"context"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/internal/cluster"
	"github.com/hyp3rd/hypercache/pkg/backend"
	cachev2 "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// TestDistMemoryStaleQuorum ensures quorum read returns newest version and repairs stale replicas.
func TestDistMemoryStaleQuorum(t *testing.T) {
	ring := cluster.NewRing(cluster.WithReplication(3))
	membership := cluster.NewMembership(ring)
	transport := backend.NewInProcessTransport()

	n1 := cluster.NewNode("", "n1:0")
	n2 := cluster.NewNode("", "n2:0")
	n3 := cluster.NewNode("", "n3:0")

	b1i, _ := backend.NewDistMemory(context.TODO(), backend.WithDistMembership(membership, n1), backend.WithDistTransport(transport), backend.WithDistReadConsistency(backend.ConsistencyQuorum))
	b2i, _ := backend.NewDistMemory(context.TODO(), backend.WithDistMembership(membership, n2), backend.WithDistTransport(transport), backend.WithDistReadConsistency(backend.ConsistencyQuorum))
	b3i, _ := backend.NewDistMemory(context.TODO(), backend.WithDistMembership(membership, n3), backend.WithDistTransport(transport), backend.WithDistReadConsistency(backend.ConsistencyQuorum))

	b1 := b1i.(*backend.DistMemory) //nolint:forcetypeassert
	b2 := b2i.(*backend.DistMemory) //nolint:forcetypeassert
	b3 := b3i.(*backend.DistMemory) //nolint:forcetypeassert

	transport.Register(b1)
	transport.Register(b2)
	transport.Register(b3)

	key := "sq-key"

	owners := ring.Lookup(key)
	if len(owners) != 3 {
		t.Skip("replication factor !=3")
	}

	// Write initial version via primary
	primary := owners[0]
	item := &cachev2.Item{Key: key, Value: "v1"}

	_ = item.Valid()
	if primary == b1.LocalNodeID() {
		_ = b1.Set(context.Background(), item)
	} else if primary == b2.LocalNodeID() {
		_ = b2.Set(context.Background(), item)
	} else {
		_ = b3.Set(context.Background(), item)
	}

	// Manually bump version on one replica to simulate a newer write that others missed
	// Pick owners[1] as ahead replica
	aheadID := owners[1]
	ahead := map[cluster.NodeID]*backend.DistMemory{b1.LocalNodeID(): b1, b2.LocalNodeID(): b2, b3.LocalNodeID(): b3}[aheadID]
	ahead.DebugInject(&cachev2.Item{Key: key, Value: "v2", Version: 5, Origin: string(ahead.LocalNodeID()), LastUpdated: time.Now()})

	// Drop local copy on owners[2] to simulate stale/missing
	lagID := owners[2]
	lag := map[cluster.NodeID]*backend.DistMemory{b1.LocalNodeID(): b1, b2.LocalNodeID(): b2, b3.LocalNodeID(): b3}[lagID]
	lag.DebugDropLocal(key)

	// Issue quorum read from a non-ahead node (choose primary if not ahead, else third)
	requester := b1
	if requester.LocalNodeID() == aheadID {
		requester = b2
	}

	if requester.LocalNodeID() == aheadID {
		requester = b3
	}

	got, ok := requester.Get(context.Background(), key)
	if !ok {
		t.Fatalf("quorum get failed")
	}
	// Value stored as interface{} may be string (not []byte) in this test
	if sval, okCast := got.Value.(string); !okCast || sval != "v2" {
		t.Fatalf("expected quorum to return newer version v2, got=%v (type %T)", got.Value, got.Value)
	}

	// Allow brief repair propagation
	time.Sleep(50 * time.Millisecond)

	// All owners should now have v2 (version 5)
	for _, oid := range owners {
		inst := map[cluster.NodeID]*backend.DistMemory{b1.LocalNodeID(): b1, b2.LocalNodeID(): b2, b3.LocalNodeID(): b3}[oid]

		it, ok2 := inst.Get(context.Background(), key)
		if !ok2 || it.Version != 5 {
			t.Fatalf("owner %s not repaired to v2 (v5) -> (%v,%v)", oid, ok2, it)
		}
	}

	// ReadRepair metric should have incremented somewhere
	if b1.Metrics().ReadRepair+b2.Metrics().ReadRepair+b3.Metrics().ReadRepair == 0 {
		t.Fatalf("expected read repair metric >0")
	}
}
