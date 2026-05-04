package tests

import (
	"context"
	"testing"

	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// TestDistMemoryRemoveReplication ensures that Remove replicates deletions across replicas.
func TestDistMemoryRemoveReplication(t *testing.T) {
	t.Parallel()

	dc := SetupInProcessClusterRF(t, 2, 2)

	const key = "remove-key"

	owners := dc.Ring.Lookup(key)
	if len(owners) == 0 {
		t.Fatalf("no owners")
	}

	item := &cache.Item{Key: key, Value: "val"}

	err := item.Valid()
	if err != nil {
		t.Fatalf("valid: %v", err)
	}

	primary := dc.ByID(owners[0])

	err = primary.Set(context.Background(), item)
	if err != nil {
		t.Fatalf("set: %v", err)
	}

	if _, ok := dc.Nodes[0].Get(context.Background(), key); !ok {
		t.Fatalf("b1 missing pre-remove")
	}

	if _, ok := dc.Nodes[1].Get(context.Background(), key); !ok {
		t.Fatalf("b2 missing pre-remove")
	}

	err = primary.Remove(context.Background(), key)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}

	if _, ok := dc.Nodes[0].Get(context.Background(), key); ok {
		t.Fatalf("b1 still has key after remove")
	}

	if _, ok := dc.Nodes[1].Get(context.Background(), key); ok {
		t.Fatalf("b2 still has key after remove")
	}
}

// TestDistMemoryReadRepair ensures a stale replica is healed on read.
func TestDistMemoryReadRepair(t *testing.T) {
	t.Parallel()

	dc := SetupInProcessClusterRF(t, 2, 2)

	const key = "rr-key"

	// Setup uses RF=2 with 2 nodes registered — Lookup must return both.
	// A <2 result indicates a test-environment regression rather than a
	// benign condition.
	owners := dc.Ring.Lookup(key)
	if len(owners) < 2 {
		t.Fatalf("expected >=2 owners with RF=2 setup, got %d", len(owners))
	}

	item := &cache.Item{Key: key, Value: "val"}

	err := item.Valid()
	if err != nil {
		t.Fatalf("valid: %v", err)
	}

	primary := dc.ByID(owners[0])

	err = primary.Set(context.Background(), item)
	if err != nil {
		t.Fatalf("set: %v", err)
	}

	replicaNode := dc.ByID(owners[1])
	replicaNode.DebugDropLocal(key)

	if replicaNode.LocalContains(key) {
		t.Fatalf("replica still has key after drop")
	}

	// Issue Get from the replica side so the read fans out to the primary,
	// which surfaces the missing entry and triggers read-repair.
	requester := replicaNode

	if _, ok := requester.Get(context.Background(), key); !ok {
		t.Fatalf("get for read-repair failed")
	}

	if !primary.LocalContains(key) {
		t.Fatalf("primary missing after read repair")
	}

	if !replicaNode.LocalContains(key) {
		t.Fatalf("replica missing after read repair")
	}

	if replicaNode.Metrics().ReadRepair == 0 {
		t.Fatalf("expected read-repair metric increment")
	}
}
