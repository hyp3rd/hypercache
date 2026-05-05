package integration

import (
	"context"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// seedSpecCluster bundles a 3-node test cluster with the (id@addr)
// seed wiring shared by every node. Returned to the test so the
// test body stays focused on the propagation assertion.
type seedSpecCluster struct {
	a, b, c *backend.DistMemory
}

// buildSeedSpecCluster builds three HTTP-transport nodes wired with
// `id@addr` seeds — pre-fix, this configuration produced a broken
// ring; post-fix, every node knows every peer's identity from the
// first lookup. Constructor uses context.Background() internally
// (the constructor ctx is only used for listener bind, not for any
// caller-cancellable work); test code drives Set/Get with its own
// context.
func buildSeedSpecCluster(t *testing.T) *seedSpecCluster {
	t.Helper()

	ctx := context.Background()

	addrA := allocatePort(t)
	addrB := allocatePort(t)
	addrC := allocatePort(t)

	all := []struct{ id, addr string }{
		{"node-A", addrA}, {"node-B", addrB}, {"node-C", addrC},
	}

	seedsFor := func(self string) []string {
		out := make([]string, 0, len(all)-1)

		for _, s := range all {
			if s.id == self {
				continue
			}

			out = append(out, s.id+"@"+s.addr)
		}

		return out
	}

	mkNode := func(id, addr string) *backend.DistMemory {
		bm, err := backend.NewDistMemory(
			ctx,
			backend.WithDistNode(id, addr),
			backend.WithDistSeeds(seedsFor(id)),
			backend.WithDistReplication(3),
			backend.WithDistVirtualNodes(32),
			backend.WithDistReadConsistency(backend.ConsistencyOne),
			backend.WithDistWriteConsistency(backend.ConsistencyQuorum),
		)
		if err != nil {
			t.Fatalf("new node %s: %v", id, err)
		}

		dm, ok := bm.(*backend.DistMemory)
		if !ok {
			t.Fatalf("cast %s: %T", id, bm)
		}

		t.Cleanup(func() { _ = dm.Stop(ctx) })

		return dm
	}

	cluster := &seedSpecCluster{
		a: mkNode("node-A", addrA),
		b: mkNode("node-B", addrB),
		c: mkNode("node-C", addrC),
	}

	for _, addr := range []string{addrA, addrB, addrC} {
		waitForDistNodeHealth(ctx, t, addr)
	}

	// Allow a brief settle so the auto-created HTTP transport has
	// resolved peer URLs from membership before the first Set
	// fans out. 200 ms is generous on a single host.
	time.Sleep(200 * time.Millisecond)

	return cluster
}

// TestDistSeedSpec_PropagatesAcrossNodes is the end-to-end fix
// regression for the cluster-propagation bug: pre-fix, seeds were
// added to membership with empty IDs, the ring built over empty IDs,
// every node treated itself as the only real owner, and writes
// stopped at whichever node received them. The fix: seeds may now
// carry their peer's node ID inline as `id@addr`.
func TestDistSeedSpec_PropagatesAcrossNodes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cl := buildSeedSpecCluster(t)

	const key = "propagation-key"

	err := cl.a.Set(ctx, &cache.Item{
		Key:         key,
		Value:       []byte("hello"),
		Version:     1,
		Origin:      "node-A",
		LastUpdated: time.Now(),
	})
	if err != nil {
		t.Fatalf("set on A: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for _, n := range []*backend.DistMemory{cl.b, cl.c} {
		found := false

		for time.Now().Before(deadline) {
			it, ok := n.Get(ctx, key)
			if ok && it != nil {
				found = true

				break
			}

			time.Sleep(50 * time.Millisecond)
		}

		if !found {
			t.Fatalf("node %s never saw the propagated key", n.LocalNodeID())
		}
	}
}
