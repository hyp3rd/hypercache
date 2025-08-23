package tests

import (
	"context"
	"testing"
	"time"

	backend "github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// TestMerkleDeleteTombstone ensures a deleted key does not resurrect via sync.
func TestMerkleDeleteTombstone(t *testing.T) {
	ctx := context.Background()
	transport := backend.NewInProcessTransport()

	a, _ := backend.NewDistMemory(ctx, backend.WithDistNode("A", "127.0.0.1:9501"), backend.WithDistReplication(1), backend.WithDistMerkleChunkSize(2))
	b, _ := backend.NewDistMemory(ctx, backend.WithDistNode("B", "127.0.0.1:9502"), backend.WithDistReplication(1), backend.WithDistMerkleChunkSize(2))

	da := any(a).(*backend.DistMemory)
	db := any(b).(*backend.DistMemory)
	da.SetTransport(transport)
	db.SetTransport(transport)
	transport.Register(da)
	transport.Register(db)

	it := &cache.Item{Key: "del", Value: []byte("v"), Version: 1, Origin: "A", LastUpdated: time.Now()}
	da.DebugInject(it)
	if err := db.SyncWith(ctx, string(da.LocalNodeID())); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	// Now delete on A
	_ = da.Remove(ctx, "del")

	// Ensure local A removed
	if val, _ := da.Get(ctx, "del"); val != nil {
		t.Fatalf("expected A delete")
	}

	// Remote (B) pulls from A to learn about deletion (pull-based anti-entropy)
	if err := db.SyncWith(ctx, string(da.LocalNodeID())); err != nil {
		t.Fatalf("tomb sync pull: %v", err)
	}

	// Ensure B removed or will not resurrect on next sync
	if val, _ := db.Get(ctx, "del"); val != nil {
		t.Fatalf("expected B delete after tombstone")
	}

	// Re-add older version on B (simulate stale write)
	stale := &cache.Item{Key: "del", Value: []byte("stale"), Version: 1, Origin: "B", LastUpdated: time.Now()}
	db.DebugInject(stale)

	// Sync B with A again; B should keep deletion (not resurrect)
	if err := db.SyncWith(ctx, string(da.LocalNodeID())); err != nil {
		t.Fatalf("resync: %v", err)
	}
	if val, _ := db.Get(ctx, "del"); val != nil {
		t.Fatalf("tombstone failed; key resurrected")
	}
}
