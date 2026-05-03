package tests

import (
	"context"
	"slices"
	"testing"

	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// TestDistMemoryForwardingReplication spins up two DistMemory backends sharing membership and transport
// then ensures ownership, forwarding and replication semantics hold.
func TestDistMemoryForwardingReplication(t *testing.T) {
	t.Parallel()

	dc := SetupInProcessClusterRF(t, 2, 2)

	keys := []string{"alpha", "bravo", "charlie", "delta", "echo"}

	// Write via the primary owner to guarantee placement + replication.
	for _, k := range keys {
		owners := dc.Ring.Lookup(k)
		if len(owners) == 0 {
			t.Fatalf("no owners for key %s", k)
		}

		item := &cache.Item{Key: k, Value: k}

		err := item.Valid()
		if err != nil {
			t.Fatalf("item valid %s: %v", k, err)
		}

		primary := dc.ByID(owners[0])
		if primary == nil {
			t.Fatalf("unexpected owner id %s", owners[0])
		}

		err = primary.Set(context.Background(), item)
		if err != nil {
			t.Fatalf("set %s via %s: %v", k, owners[0], err)
		}
	}

	// Each key should be readable via either owner (primary forward) or local if replica.
	for _, k := range keys {
		if _, ok := dc.Nodes[0].Get(context.Background(), k); !ok {
			t.Fatalf("b1 cannot get key %s", k)
		}

		if _, ok := dc.Nodes[1].Get(context.Background(), k); !ok {
			t.Fatalf("b2 cannot get key %s", k)
		}
	}

	// Replication check: at least one key should physically exist on b2.
	if !slices.ContainsFunc(keys, dc.Nodes[1].LocalContains) {
		t.Fatalf("expected at least one replicated key on b2")
	}
}
