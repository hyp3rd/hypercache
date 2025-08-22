package tests

import (
	"context"
	"slices"
	"testing"

	"github.com/hyp3rd/hypercache/internal/cluster"
	"github.com/hyp3rd/hypercache/pkg/backend"
	cachev2 "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// TestDistMemoryForwardingReplication spins up two DistMemory backends sharing membership and transport
// then ensures ownership, forwarding and replication semantics hold.
func TestDistMemoryForwardingReplication(t *testing.T) {
	ring := cluster.NewRing(cluster.WithReplication(2))
	membership := cluster.NewMembership(ring)
	transport := backend.NewInProcessTransport()

	// create two nodes/backends
	n1 := cluster.NewNode("", "node1:0")
	n2 := cluster.NewNode("", "node2:0")

	b1i, err := backend.NewDistMemory(
		context.TODO(),
		backend.WithDistMembership(membership, n1),
		backend.WithDistTransport(transport),
	)
	if err != nil {
		t.Fatalf("backend1: %v", err)
	}
	b2i, err := backend.NewDistMemory(
		context.TODO(),
		backend.WithDistMembership(membership, n2),
		backend.WithDistTransport(transport),
	)
	if err != nil {
		t.Fatalf("backend2: %v", err)
	}
	b1 := b1i.(*backend.DistMemory) //nolint:forcetypeassert
	b2 := b2i.(*backend.DistMemory) //nolint:forcetypeassert

	transport.Register(b1)
	transport.Register(b2)

	// pick keys to exercise distribution (simple deterministic list)
	keys := []string{"alpha", "bravo", "charlie", "delta", "echo"}
	// write via the node that is primary owner to guarantee placement + replication
	for _, k := range keys {
		owners := ring.Lookup(k)
		if len(owners) == 0 {
			t.Fatalf("no owners for key %s", k)
		}
		item := &cachev2.Item{Key: k, Value: k}
		if err := item.Valid(); err != nil {
			t.Fatalf("item valid %s: %v", k, err)
		}
		target := owners[0]
		var err2 error
		switch target {
		case n1.ID:
			err2 = b1.Set(context.Background(), item)
		case n2.ID:
			err2 = b2.Set(context.Background(), item)
		default:
			t.Fatalf("unexpected owner id %s", target)
		}
		if err2 != nil {
			t.Fatalf("set %s via %s: %v", k, target, err2)
		}
	}

	// Each key should be readable via either owner (b1 primary forward) or local if replica
	for _, k := range keys {
		if _, ok := b1.Get(context.Background(), k); !ok {
			t.Fatalf("b1 cannot get key %s", k)
		}
		if _, ok := b2.Get(context.Background(), k); !ok { // should forward or local hit
			t.Fatalf("b2 cannot get key %s", k)
		}
	}

	// Check replication: at least one key should physically exist on b2 after writes from b1 when b2 is replica
	foundReplica := slices.ContainsFunc(keys, b2.LocalContains)
	if !foundReplica {
		t.Fatalf("expected at least one replicated key on b2")
	}
}
