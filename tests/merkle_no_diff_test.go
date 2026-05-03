package tests

import (
	"context"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// TestMerkleNoDiff ensures SyncWith returns quickly when trees are identical.
func TestMerkleNoDiff(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	transport := backend.NewInProcessTransport()

	a, _ := backend.NewDistMemory(
		ctx,
		backend.WithDistNode("A", AllocatePort(t)),
		backend.WithDistReplication(1),
		backend.WithDistMerkleChunkSize(4),
	)
	b, _ := backend.NewDistMemory(
		ctx,
		backend.WithDistNode("B", AllocatePort(t)),
		backend.WithDistReplication(1),
		backend.WithDistMerkleChunkSize(4),
	)

	da, ok := any(a).(*backend.DistMemory)
	if !ok {
		t.Fatalf("failed to cast a to *backend.DistMemory")
	}

	db, ok := any(b).(*backend.DistMemory)
	if !ok {
		t.Fatalf("failed to cast b to *backend.DistMemory")
	}

	StopOnCleanup(t, da)
	StopOnCleanup(t, db)

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

	err := da.SyncWith(ctx, string(db.LocalNodeID()))
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	err = db.SyncWith(ctx, string(da.LocalNodeID()))
	if err != nil {
		t.Fatalf("sync2: %v", err)
	}
}
