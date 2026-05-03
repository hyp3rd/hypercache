package tests

import (
	"context"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// pickNonAheadRequester returns the first cluster node that is not the
// `ahead` replica — quorum-read tests need the read to come from a lagging
// or correct node so the quorum fan-out forces reconciliation.
func pickNonAheadRequester(dc *DistCluster, ahead *backend.DistMemory) *backend.DistMemory {
	for _, n := range dc.Nodes {
		if n.LocalNodeID() != ahead.LocalNodeID() {
			return n
		}
	}

	return dc.Nodes[0]
}

// TestDistMemoryStaleQuorum ensures quorum read returns newest version and repairs stale replicas.
func TestDistMemoryStaleQuorum(t *testing.T) {
	t.Parallel()

	dc := SetupInProcessCluster(t, 3,
		backend.WithDistReadConsistency(backend.ConsistencyQuorum),
	)

	const key = "sq-key"

	owners := dc.Ring.Lookup(key)
	if len(owners) != 3 {
		t.Skip("replication factor !=3")
	}

	item := &cache.Item{Key: key, Value: "v1"}

	_ = item.Valid()
	_ = dc.ByID(owners[0]).Set(context.Background(), item)

	// Bump version on owners[1] to simulate a newer write the others missed.
	ahead := dc.ByID(owners[1])
	ahead.DebugInject(&cache.Item{
		Key: key, Value: "v2", Version: 5,
		Origin: string(ahead.LocalNodeID()), LastUpdated: time.Now(),
	})
	dc.ByID(owners[2]).DebugDropLocal(key)

	got, ok := pickNonAheadRequester(dc, ahead).Get(context.Background(), key)
	if !ok {
		t.Fatalf("quorum get failed")
	}

	sval, okCast := got.Value.(string)
	if !okCast || sval != "v2" {
		t.Fatalf("expected quorum to return newer version v2, got=%v (type %T)", got.Value, got.Value)
	}

	// Brief sleep to let the read-repair fan-out land on lagging replicas.
	time.Sleep(50 * time.Millisecond)

	for _, oid := range owners {
		it, ok2 := dc.ByID(oid).Get(context.Background(), key)
		if !ok2 || it.Version != 5 {
			t.Fatalf("owner %s not repaired to v2 (v5) -> (%v,%v)", oid, ok2, it)
		}
	}

	totalRepair := dc.Nodes[0].Metrics().ReadRepair +
		dc.Nodes[1].Metrics().ReadRepair +
		dc.Nodes[2].Metrics().ReadRepair
	if totalRepair == 0 {
		t.Fatalf("expected read repair metric >0")
	}
}
