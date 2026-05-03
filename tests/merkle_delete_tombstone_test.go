package tests

import (
	"context"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// TestMerkleDeleteTombstone ensures a deleted key does not resurrect via sync.
func TestMerkleDeleteTombstone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	transport := backend.NewInProcessTransport()

	da := newMerkleNode(t, transport, "A")
	db := newMerkleNode(t, transport, "B")

	it := &cache.Item{Key: "del", Value: []byte("v"), Version: 1, Origin: "A", LastUpdated: time.Now()}
	da.DebugInject(it)

	err := db.SyncWith(ctx, string(da.LocalNodeID()))
	if err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	_ = da.Remove(ctx, "del")

	if val, _ := da.Get(ctx, "del"); val != nil {
		t.Fatalf("expected A delete")
	}

	// B pulls from A to learn the deletion (pull-based anti-entropy).
	err = db.SyncWith(ctx, string(da.LocalNodeID()))
	if err != nil {
		t.Fatalf("tomb sync pull: %v", err)
	}

	if val, _ := db.Get(ctx, "del"); val != nil {
		t.Fatalf("expected B delete after tombstone")
	}

	// Re-add older version on B (simulate stale write).
	stale := &cache.Item{Key: "del", Value: []byte("stale"), Version: 1, Origin: "B", LastUpdated: time.Now()}
	db.DebugInject(stale)

	err = db.SyncWith(ctx, string(da.LocalNodeID()))
	if err != nil {
		t.Fatalf("resync: %v", err)
	}

	if val, _ := db.Get(ctx, "del"); val != nil {
		t.Fatalf("tombstone failed; key resurrected")
	}
}
