package tests

import (
	"context"
	"testing"

	"github.com/hyp3rd/hypercache/pkg/backend"
)

// TestMerkleEmptyTrees ensures diff between two empty trees is empty and SyncWith is no-op.
func TestMerkleEmptyTrees(t *testing.T) {
	ctx := context.Background()
	transport := backend.NewInProcessTransport()

	a, _ := backend.NewDistMemory(
		ctx,
		backend.WithDistNode("A", AllocatePort(t)),
		backend.WithDistReplication(1),
		backend.WithDistMerkleChunkSize(2),
	)
	b, _ := backend.NewDistMemory(
		ctx,
		backend.WithDistNode("B", AllocatePort(t)),
		backend.WithDistReplication(1),
		backend.WithDistMerkleChunkSize(2),
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

	err := da.SyncWith(ctx, string(db.LocalNodeID()))
	if err != nil {
		t.Fatalf("sync empty: %v", err)
	}

	err = db.SyncWith(ctx, string(da.LocalNodeID()))
	if err != nil {
		t.Fatalf("sync empty reverse: %v", err)
	}
}
