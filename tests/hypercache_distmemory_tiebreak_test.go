package tests

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/internal/cluster"
	"github.com/hyp3rd/hypercache/pkg/backend"
	v2 "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// TestDistMemoryVersionTieBreak ensures that when versions are equal the lexicographically smaller origin wins.
func TestDistMemoryVersionTieBreak(t *testing.T) { //nolint:paralleltest
	interval := 5 * time.Millisecond
	ring := cluster.NewRing(cluster.WithReplication(3))
	membership := cluster.NewMembership(ring)
	transport := backend.NewInProcessTransport()

	n1 := cluster.NewNode("", "n1:0")
	n2 := cluster.NewNode("", "n2:0")
	n3 := cluster.NewNode("", "n3:0")

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

	// choose key where b1,b2,b3 ordering fixed
	key := "tie"
	for i := range 3000 {
		cand := fmt.Sprintf("tie%d", i)

		owners := b1.DebugOwners(cand)
		if len(owners) == 3 && owners[0] == b1.LocalNodeID() && owners[1] == b2.LocalNodeID() && owners[2] == b3.LocalNodeID() {
			key = cand

			break
		}
	}

	// primary write to establish version=1 origin=b1
	err := b1.Set(context.Background(), &v2.Item{Key: key, Value: "v1"})
	if err != nil {
		t.Fatalf("initial set: %v", err)
	}

	// Inject a fake item on b2 with SAME version but lexicographically larger origin so it should lose.
	b2.DebugDropLocal(key)
	b2.DebugInject(&v2.Item{Key: key, Value: "alt", Version: 1, Origin: "zzzz"})

	// Quorum read through b3 triggers selection + repair.
	it, ok := b3.Get(context.Background(), key)
	if !ok {
		t.Fatalf("expected quorum read ok")
	}

	if it.Value != "v1" {
		t.Fatalf("expected b1 value win, got %v", it.Value)
	}

	// Ensure b2 repaired to winning value.
	if it2, ok2 := b2.Get(context.Background(), key); !ok2 || it2.Value != "v1" {
		t.Fatalf("expected repaired tie-break value on b2")
	}
}
