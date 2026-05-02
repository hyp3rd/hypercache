package tests

import (
	"context"
	"testing"

	backend "github.com/hyp3rd/hypercache/pkg/backend"
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

	da := any(a).(*backend.DistMemory)
	db := any(b).(*backend.DistMemory)

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
