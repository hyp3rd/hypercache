package tests

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// rebalanceStressDuration is the wall-clock window each test waits
// before sampling the counters. Long enough for the configured
// rebalance ticker (50ms) to fire ~20 times — pre-fix that window
// produced double-digit counter values per node, post-fix the
// counters either stay at zero or settle at exactly one bump per
// stuck key and never advance again.
const rebalanceStressDuration = time.Second

// TestDistRebalance_IdleClusterIsSilent pins the steady-state
// resilience contract: a cluster with no membership changes and
// no out-of-place keys must produce zero rebalance counter bumps,
// regardless of how many ticks fire. Operators on the
// hypercache-monitor /metrics page should see all rebalance tiles
// flat at zero unless a node is actively joining or leaving.
func TestDistRebalance_IdleClusterIsSilent(t *testing.T) {
	t.Parallel()

	dc := SetupInProcessClusterRF(
		t, 5, 3,
		backend.WithDistRebalanceInterval(50*time.Millisecond),
		backend.WithDistHeartbeat(20*time.Millisecond, 200*time.Millisecond, 500*time.Millisecond),
		backend.WithDistGossipInterval(30*time.Millisecond),
	)

	ctx := context.Background()

	for i := range 20 {
		err := dc.Nodes[0].Set(ctx, &cache.Item{
			Key:   "idle-" + cacheKeySuffix(i),
			Value: "v",
		})
		if err != nil {
			t.Fatalf("seed Set #%d: %v", i, err)
		}
	}

	time.Sleep(rebalanceStressDuration)

	for i, node := range dc.Nodes {
		m := node.Metrics()
		if m.RebalancedKeys != 0 {
			t.Errorf("node %d RebalancedKeys: got %d, want 0", i, m.RebalancedKeys)
		}

		if m.RebalancedPrimary != 0 {
			t.Errorf("node %d RebalancedPrimary: got %d, want 0", i, m.RebalancedPrimary)
		}

		if m.RebalanceBatches != 0 {
			t.Errorf("node %d RebalanceBatches: got %d, want 0", i, m.RebalanceBatches)
		}

		if m.RebalancedReplicaDiff != 0 {
			t.Errorf("node %d RebalancedReplicaDiff: got %d, want 0", i, m.RebalancedReplicaDiff)
		}
	}
}

// TestDistRebalance_IdleClusterUnderTrafficIsSilent extends the
// idle-cluster guarantee to a more realistic shape: keep writing
// for several rebalance ticks and confirm the counters still stay
// at zero. The user-reported numbers (RebalancedPrimary 13k+
// climbing 60/s in a 5-node cluster) implied something about
// active workload was driving stuck keys; this test pins that
// concurrent write traffic against owned keys does NOT itself
// produce candidates.
func TestDistRebalance_IdleClusterUnderTrafficIsSilent(t *testing.T) {
	t.Parallel()

	dc := SetupInProcessClusterRF(
		t, 5, 3,
		backend.WithDistRebalanceInterval(50*time.Millisecond),
		backend.WithDistHeartbeat(20*time.Millisecond, 200*time.Millisecond, 500*time.Millisecond),
		backend.WithDistGossipInterval(30*time.Millisecond),
	)

	ctx := context.Background()
	stop := make(chan struct{})
	done := make(chan struct{})

	// Keep writing throughout the observation window.
	go func() {
		defer close(done)

		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}

			err := dc.Nodes[i%len(dc.Nodes)].Set(ctx, &cache.Item{
				Key:   "traffic-" + cacheKeySuffix(i),
				Value: "v",
			})
			if err != nil {
				return
			}

			i++

			time.Sleep(2 * time.Millisecond) // ~500 writes/sec
		}
	}()

	time.Sleep(2 * rebalanceStressDuration)

	close(stop)
	<-done

	for i, node := range dc.Nodes {
		m := node.Metrics()
		if m.RebalancedKeys != 0 {
			t.Errorf("node %d RebalancedKeys under write traffic: got %d, want 0", i, m.RebalancedKeys)
		}

		if m.RebalancedPrimary != 0 {
			t.Errorf("node %d RebalancedPrimary under write traffic: got %d, want 0", i, m.RebalancedPrimary)
		}
	}
}

// TestDistRebalance_LostOwnershipDrainsOnce pins the corrective
// behavior for keys that ARE legitimately out of place — e.g. a
// node bootstrapped as a singleton, accepted writes, and then
// ceded ownership to joining peers, so its shard now holds keys
// the ring no longer assigns to it.
//
// Two things must hold:
//
//  1. Each stuck key produces EXACTLY ONE migration (a wave that
//     drains on the first rebalance tick that sees them).
//  2. After draining, the counters must stay constant — re-flagging
//     the same key on every subsequent tick (the bug) would show as
//     a monotonically rising counter under the second observation
//     window. Pre-fix, with `removalGracePeriod == 0`, the local
//     copy was never released and the migration kept firing.
func TestDistRebalance_LostOwnershipDrainsOnce(t *testing.T) {
	t.Parallel()

	dc := SetupInProcessClusterRF(
		t, 5, 3,
		backend.WithDistRebalanceInterval(50*time.Millisecond),
	)

	// Plant one stuck key per node by injecting into the shards of
	// nodes that are NOT in the ring's owner set for that key.
	stuckPerNode := make(map[int]int)

	for i, node := range dc.Nodes {
		key := "stuck-" + cacheKeySuffix(i)

		owners := dc.Ring.Lookup(key)
		ownedByThisNode := slices.Contains(owners, node.LocalNodeID())

		if !ownedByThisNode {
			node.DebugInject(&cache.Item{Key: key, Value: "stuck"})

			stuckPerNode[i] = 1
		}
	}

	if len(stuckPerNode) == 0 {
		t.Fatalf("test fixture didn't produce any stuck keys (all keys hashed onto their host node)")
	}

	// First observation: let several ticks fire so the drain
	// completes. Each stuck key should produce exactly one
	// migration.
	time.Sleep(rebalanceStressDuration)

	afterDrain := make(map[int]backend.DistMetrics, len(dc.Nodes))
	for i, node := range dc.Nodes {
		afterDrain[i] = node.Metrics()
	}

	for i, want := range stuckPerNode {
		got := afterDrain[i].RebalancedPrimary
		if got != int64(want) {
			t.Errorf("node %d RebalancedPrimary after drain: got %d, want %d (one migration per stuck key)",
				i, got, want)
		}
	}

	// Second observation: keep ticking. The counters must NOT
	// advance — the stuck keys were drained, the local copies are
	// gone, and there's nothing left for the scan to flag.
	time.Sleep(rebalanceStressDuration)

	for i, node := range dc.Nodes {
		got := node.Metrics().RebalancedPrimary
		want := afterDrain[i].RebalancedPrimary

		if got != want {
			t.Errorf("node %d RebalancedPrimary advanced after drain: was %d, now %d (re-flag bug)",
				i, want, got)
		}
	}
}

// cacheKeySuffix is a small itoa-shim that avoids dragging strconv
// just for tests; mirrors keyfmt's style used by the
// dist_read_repair unit tests.
func cacheKeySuffix(i int) string {
	if i == 0 {
		return "0"
	}

	out := ""
	for i > 0 {
		out = string(rune('0'+(i%10))) + out

		i /= 10
	}

	return out
}

// TestDistRebalance_ApplyOwnershipGuardRefusesForeignWrites pins
// the receiver-side guard introduced for the persistent-divergent-
// ring-view scenario: when a peer Forwards a Set for a key whose
// owners list (per the receiver's view) doesn't include the
// receiver, the receiver MUST silently drop the write instead of
// planting it. Without this guard, the migration target's
// replicate-fan-out re-plants stuck keys on the original source on
// every rebalance tick — even after the source releases the local
// copy — because the target's view still treats the source as a
// replica. The pre-fix loop: source-migrates → target-fan-outs-back
// → source-stuck-again → next-tick-migrates → ... at 60 keys/sec.
//
// Reproducer: simulate a divergent view directly by issuing a
// ForwardSet to a node we know is NOT in the key's owner set.
func TestDistRebalance_ApplyOwnershipGuardRefusesForeignWrites(t *testing.T) {
	t.Parallel()

	dc := SetupInProcessClusterRF(t, 5, 3)

	const key = "guard-key-target-must-not-store"

	owners := dc.Ring.Lookup(key)

	var nonOwner *backend.DistMemory

	for _, node := range dc.Nodes {
		isOwner := slices.Contains(owners, node.LocalNodeID())

		if !isOwner {
			nonOwner = node

			break
		}
	}

	if nonOwner == nil {
		t.Fatalf("test fixture didn't produce a non-owner node for key %q", key)
	}

	// Issue a Forward directly to the non-owner via the shared
	// in-process transport. Pre-guard this planted the item on the
	// non-owner's shard; post-guard the call returns nil but the
	// shard stays clean and the refused-counter ticks up.
	err := dc.Transport.ForwardSet(context.Background(), string(nonOwner.LocalNodeID()),
		&cache.Item{Key: key, Value: "foreign"}, false)
	if err != nil {
		t.Fatalf("ForwardSet returned an error: %v (best-effort contract requires nil)", err)
	}

	if nonOwner.LocalContains(key) {
		t.Errorf("non-owner stored a foreign write: ownership guard didn't fire")
	}

	if got := nonOwner.Metrics().WriteApplyRefused; got == 0 {
		t.Errorf("WriteApplyRefused: got 0, want > 0 (guard didn't bump its counter)")
	}
}
