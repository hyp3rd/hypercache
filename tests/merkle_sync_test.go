package tests

import (
	"context"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// TestMerkleSyncConvergence ensures SyncWith pulls newer keys from remote.
func TestMerkleSyncConvergence(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	transport := backend.NewInProcessTransport()

	dmA := newMerkleNode(t, transport, "A")
	dmB := newMerkleNode(t, transport, "B")

	// Inject divergent data: A holds 5 newer keys, B holds older copies of the first 2.
	for i := range 5 {
		it := &cache.Item{Key: keyf("k", i), Value: []byte("vA"), Version: uint64(i + 1), Origin: "A", LastUpdated: time.Now()}
		dmA.DebugInject(it)
	}

	for i := range 2 {
		it := &cache.Item{Key: keyf("k", i), Value: []byte("old"), Version: uint64(i), Origin: "B", LastUpdated: time.Now()}
		dmB.DebugInject(it)
	}

	syncErr := dmB.SyncWith(ctx, string(dmA.LocalNodeID()))
	if syncErr != nil && testing.Verbose() {
		// HTTP transport fetch merkle unsupported; we rely on in-process
		t.Logf("sync error: %v", syncErr)
	}

	// Validate B now has all 5 keys with correct versions (>= A's).
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

func keyf(prefix string, i int) string {
	return prefix + ":" + string(rune('a'+i)) //nolint:gosec // test fixture, i bounded in callers
}
