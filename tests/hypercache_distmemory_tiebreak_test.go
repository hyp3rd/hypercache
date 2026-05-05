package tests

import (
	"context"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/internal/cluster"
	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// TestDistMemoryVersionTieBreak ensures that when versions are equal the lexicographically smaller origin wins.
func TestDistMemoryVersionTieBreak(t *testing.T) { //nolint:paralleltest // mutates shared transport
	const interval = 5 * time.Millisecond

	dc := SetupInProcessCluster(
		t, 3,
		backend.WithDistReplication(3),
		backend.WithDistHeartbeat(interval, 0, 0),
		backend.WithDistReadConsistency(backend.ConsistencyQuorum),
		backend.WithDistWriteConsistency(backend.ConsistencyQuorum),
	)

	b1, b2, b3 := dc.Nodes[0], dc.Nodes[1], dc.Nodes[2]

	key := findOrderedThreeOwnerKeyPrefix(b1, []cluster.NodeID{b1.LocalNodeID(), b2.LocalNodeID(), b3.LocalNodeID()}, "tie")

	// Primary write to establish version=1, origin=b1.
	err := b1.Set(context.Background(), &cache.Item{Key: key, Value: "v1"})
	if err != nil {
		t.Fatalf("initial set: %v", err)
	}

	// Inject a fake item on b2 with the SAME version but a lexicographically
	// larger origin — under the tie-break rule it should lose.
	b2.DebugDropLocal(key)
	b2.DebugInject(&cache.Item{Key: key, Value: "alt", Version: 1, Origin: "zzzz"})

	// Quorum read through b3 triggers selection + repair.
	it, ok := b3.Get(context.Background(), key)
	if !ok {
		t.Fatalf("expected quorum read ok")
	}

	if it.Value != "v1" {
		t.Fatalf("expected b1 value win, got %v", it.Value)
	}

	if it2, ok2 := b2.Get(context.Background(), key); !ok2 || it2.Value != "v1" {
		t.Fatalf("expected repaired tie-break value on b2")
	}
}
