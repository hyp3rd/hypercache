package tests

import (
	"context"
	"testing"
	"time"

	backend "github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// TestMerkleSingleMissingKey ensures a single remote-only key is detected and pulled.
func TestMerkleSingleMissingKey(t *testing.T) {
	ctx := context.Background()
	transport := backend.NewInProcessTransport()

	a, _ := backend.NewDistMemory(ctx, backend.WithDistNode("A", "127.0.0.1:9301"), backend.WithDistReplication(1), backend.WithDistMerkleChunkSize(2))
	b, _ := backend.NewDistMemory(ctx, backend.WithDistNode("B", "127.0.0.1:9302"), backend.WithDistReplication(1), backend.WithDistMerkleChunkSize(2))

	da := any(a).(*backend.DistMemory)
	db := any(b).(*backend.DistMemory)

	da.SetTransport(transport)
	db.SetTransport(transport)
	transport.Register(da)
	transport.Register(db)

	// Inject one key only into A
	it := &cache.Item{Key: "k1", Value: []byte("v1"), Version: 1, Origin: "A", LastUpdated: time.Now()}
	da.DebugInject(it)

	err := db.SyncWith(ctx, string(da.LocalNodeID()))
	if err != nil {
		t.Fatalf("sync single: %v", err)
	}

	got, _ := db.Get(ctx, "k1")
	if got == nil {
		t.Fatalf("expected key pulled")
	}

	if bs, ok := got.Value.([]byte); !ok || string(bs) != "v1" {
		t.Fatalf("unexpected value %v", got.Value)
	}
}
