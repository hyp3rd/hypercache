package tests

import (
	"context"
	"testing"
	"time"

	backend "github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// TestMerkleNoDiff ensures SyncWith returns quickly when trees are identical.
func TestMerkleNoDiff(t *testing.T) {
	ctx := context.Background()
	transport := backend.NewInProcessTransport()

	a, _ := backend.NewDistMemory(ctx, backend.WithDistNode("A", "127.0.0.1:9401"), backend.WithDistReplication(1), backend.WithDistMerkleChunkSize(4))
	b, _ := backend.NewDistMemory(ctx, backend.WithDistNode("B", "127.0.0.1:9402"), backend.WithDistReplication(1), backend.WithDistMerkleChunkSize(4))

	da := any(a).(*backend.DistMemory)
	db := any(b).(*backend.DistMemory)
	da.SetTransport(transport)
	db.SetTransport(transport)
	transport.Register(da)
	transport.Register(db)

	// Inject identical data
	for i := range 10 {
		itA := &cache.Item{Key: keyf("nd", i), Value: []byte("v"), Version: uint64(i + 1), Origin: "A", LastUpdated: time.Now()}
		itB := &cache.Item{Key: keyf("nd", i), Value: []byte("v"), Version: uint64(i + 1), Origin: "B", LastUpdated: time.Now()}
		da.DebugInject(itA)
		db.DebugInject(itB)
	}

	if err := da.SyncWith(ctx, string(db.LocalNodeID())); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if err := db.SyncWith(ctx, string(da.LocalNodeID())); err != nil {
		t.Fatalf("sync2: %v", err)
	}
}
