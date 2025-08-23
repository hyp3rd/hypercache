package tests

import (
	"context"
	"testing"
	"time"

	backend "github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// TestMerkleSyncConvergence ensures SyncWith pulls newer keys from remote.
func TestMerkleSyncConvergence(t *testing.T) {
	ctx := context.Background()
	transport := backend.NewInProcessTransport()

	bA, err := backend.NewDistMemory(ctx,
		backend.WithDistNode("A", "127.0.0.1:9101"),
		backend.WithDistReplication(1),
		backend.WithDistMerkleChunkSize(2),
	)
	if err != nil {
		t.Fatalf("new dist memory A: %v", err)
	}

	dmA := any(bA).(*backend.DistMemory)

	bB, err := backend.NewDistMemory(ctx,
		backend.WithDistNode("B", "127.0.0.1:9102"),
		backend.WithDistReplication(1),
		backend.WithDistMerkleChunkSize(2),
	)
	if err != nil {
		t.Fatalf("new dist memory B: %v", err)
	}

	dmB := any(bB).(*backend.DistMemory)

	dmA.SetTransport(transport)
	dmB.SetTransport(transport)

	// register for in-process lookups
	transport.Register(dmA)
	transport.Register(dmB)

	// inject divergent data (A has extra/newer)
	for i := range 5 {
		it := &cache.Item{Key: keyf("k", i), Value: []byte("vA"), Version: uint64(i + 1), Origin: "A", LastUpdated: time.Now()}
		dmA.DebugInject(it)
	}

	// B shares only first 2 keys older versions
	for i := range 2 {
		it := &cache.Item{Key: keyf("k", i), Value: []byte("old"), Version: uint64(i), Origin: "B", LastUpdated: time.Now()}
		dmB.DebugInject(it)
	}

	// Run sync B->A to pull newer
	if err := dmB.SyncWith(ctx, string(dmA.LocalNodeID())); err != nil {
		// HTTP transport fetch merkle unsupported; we rely on in-process
		if testing.Verbose() {
			t.Logf("sync error: %v", err)
		}
	}

	// Validate B now has all 5 keys with correct versions (>= A's)
	for i := range 5 {
		k := keyf("k", i)
		itA, _ := dmA.Get(ctx, k)

		itB, _ := dmB.Get(ctx, k)
		if itA == nil || itB == nil {
			t.Fatalf("missing key %s after sync", k)
		}

		if itB.Version < itA.Version {
			t.Fatalf("expected B version >= A version for %s", k)
		}
	}
}

func keyf(prefix string, i int) string { return prefix + ":" + string(rune('a'+i)) }
