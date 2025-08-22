package tests

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/internal/cluster"
	"github.com/hyp3rd/hypercache/internal/sentinel"
	"github.com/hyp3rd/hypercache/pkg/backend"
	cachev2 "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// TestDistMemoryVersioningQuorum ensures higher version wins and quorum enforcement works.
func TestDistMemoryVersioningQuorum(t *testing.T) { //nolint:paralleltest
	interval := 10 * time.Millisecond
	ring := cluster.NewRing(cluster.WithReplication(3))
	membership := cluster.NewMembership(ring)
	transport := backend.NewInProcessTransport()

	// three nodes
	n1 := cluster.NewNode("", "n1:0")
	n2 := cluster.NewNode("", "n2:0")
	n3 := cluster.NewNode("", "n3:0")

	// enable quorum read + write consistency on b1
	b1i, _ := backend.NewDistMemory(
		context.TODO(),
		backend.WithDistMembership(membership, n1),
		backend.WithDistTransport(transport),
		backend.WithDistReplication(3),
		backend.WithDistHeartbeat(interval, 0, 0),
		backend.WithDistReadConsistency(backend.ConsistencyQuorum),
		backend.WithDistWriteConsistency(backend.ConsistencyQuorum),
	)
	b2i, _ := backend.NewDistMemory(context.TODO(), backend.WithDistMembership(membership, n2), backend.WithDistTransport(transport))
	b3i, _ := backend.NewDistMemory(
		context.TODO(),
		backend.WithDistMembership(membership, n3),
		backend.WithDistTransport(transport),
		backend.WithDistReadConsistency(backend.ConsistencyQuorum),
	)
	b1 := b1i.(*backend.DistMemory) //nolint:forcetypeassert
	b2 := b2i.(*backend.DistMemory) //nolint:forcetypeassert
	b3 := b3i.(*backend.DistMemory) //nolint:forcetypeassert
	transport.Register(b1)
	transport.Register(b2)
	transport.Register(b3)

	// Find a deterministic key where ownership ordering is b1,b2,b3 to avoid forwarding complexities.
	key := "k"
	for i := 0; i < 2000; i++ { // brute force
		cand := fmt.Sprintf("k%d", i)
		owners := b1.DebugOwners(cand)
		if len(owners) == 3 && owners[0] == b1.LocalNodeID() && owners[1] == b2.LocalNodeID() && owners[2] == b3.LocalNodeID() {
			key = cand
			break
		}
	}

	// Write key via primary.
	item1 := &cachev2.Item{Key: key, Value: "v1"}
	if err := b1.Set(context.Background(), item1); err != nil {
		t.Fatalf("initial set: %v", err)
	}

	// Simulate a concurrent stale write from another node with lower version (manual injection on b2).
	itemStale := &cachev2.Item{Key: key, Value: "v0", Version: 0, Origin: "zzz"}
	b2.DebugDropLocal(key)
	b2.DebugInject(itemStale)

	// Read quorum from node3: should observe latest (v1) and repair b2.
	it, ok := b3.Get(context.Background(), key)
	if !ok {
		t.Fatalf("expected read ok")
	}
	if it.Value != "v1" {
		t.Fatalf("expected value v1, got %v", it.Value)
	}

	// Ensure b2 repaired.
	if it2, ok2 := b2.Get(context.Background(), key); !ok2 || it2.Value != "v1" {
		t.Fatalf("expected repaired value on b2")
	}

	// Simulate reduced acks: unregister one replica and perform write requiring quorum (2 of 3).
	transport.Unregister(string(n3.ID))
	item2 := &cachev2.Item{Key: key, Value: "v2"}
	if err := b1.Set(context.Background(), item2); err != nil && err != sentinel.ErrQuorumFailed {
		t.Fatalf("unexpected error after replica loss: %v", err)
	}
}
