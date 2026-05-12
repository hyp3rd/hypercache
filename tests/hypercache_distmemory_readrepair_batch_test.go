package tests

import (
	"context"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// testRepairItemValue is the placeholder payload these read-repair
// fixtures store. Extracted so the package-level goconst rule has
// one shared symbol instead of the literal repeating across the
// readrepair tests in this directory.
const testRepairItemValue = "val"

// TestDistReadRepair_BatchDispatchesViaQueue is the end-to-end
// integration smoke for WithDistReadRepairBatch. It mirrors
// TestDistMemoryReadRepair's shape (set on primary, drop on
// replica, Get from replica side) but uses the batched path —
// proving the queue → flusher → ForwardSet chain actually heals
// the replica without the Get path doing the wire call inline.
//
// Polls for completion within a deadline rather than asserting
// synchronously: the batched path is async by design, so the
// "replica healed" condition is observable only AFTER the flush
// window. The poll cadence + deadline are loose enough to absorb
// CI scheduling jitter; the assertion is "convergence happens",
// not "convergence within an exact wall-clock budget".
func TestDistReadRepair_BatchDispatchesViaQueue(t *testing.T) {
	t.Parallel()

	// 3 nodes / RF=3 / ConsistencyQuorum: needed=2 acks. With one
	// replica's local copy dropped, the OTHER two owners still
	// quorum the read — and `repairReplicas` fans the chosen value
	// back across all owners via `repairRemoteReplica`, which is
	// where the queue path lives. ConsistencyOne never visits this
	// path (it self-heals locally and returns), so the queue would
	// never see an enqueue at all under the default.
	dc := SetupInProcessClusterRF(
		t, 3, 3,
		backend.WithDistReadConsistency(backend.ConsistencyQuorum),
		backend.WithDistReadRepairBatch(50*time.Millisecond, 16),
	)

	const key = "rr-batch-key"

	owners := dc.Ring.Lookup(key)
	if len(owners) < 3 {
		t.Fatalf("expected 3 owners with RF=3 setup, got %d", len(owners))
	}

	primary := dc.ByID(owners[0])
	dropped := dc.ByID(owners[1])
	reader := dc.ByID(owners[2])

	err := primary.Set(context.Background(), &cache.Item{Key: key, Value: testRepairItemValue})
	if err != nil {
		t.Fatalf("set: %v", err)
	}

	dropped.DebugDropLocal(key)

	if dropped.LocalContains(key) {
		t.Fatalf("dropped node still has key after drop")
	}

	// Get from a third owner (not the dropped one). Quorum is met
	// by primary+reader. repairReplicas then enqueues a repair for
	// the dropped node via the queue — the flush window heals it.
	if _, ok := reader.Get(context.Background(), key); !ok {
		t.Fatalf("get returned not-found")
	}

	// Poll for replica healing — bounded to 2s so a genuine
	// failure surfaces as a test fail, not a hang.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if dropped.LocalContains(key) && reader.Metrics().ReadRepairBatched > 0 {
			break
		}

		time.Sleep(10 * time.Millisecond)
	}

	if !dropped.LocalContains(key) {
		t.Errorf("dropped node did not heal within deadline (batched flush should have fired)")
	}

	if got := reader.Metrics().ReadRepairBatched; got == 0 {
		t.Errorf("ReadRepairBatched: got 0, want > 0 (queue flush didn't dispatch)")
	}
}

// TestDistReadRepair_BatchCoalescesParallelReads pins the
// amortisation win: many concurrent Gets of the same stale key
// produce ONE repair through the queue, not N. The coalescer
// keys on (peer, key) and drops same-version duplicates;
// ReadRepairCoalesced bumps for each collapsed duplicate.
func TestDistReadRepair_BatchCoalescesParallelReads(t *testing.T) {
	t.Parallel()

	// Same 3/3/Quorum shape as BatchDispatchesViaQueue — see that
	// test for the reasoning. Wider flush window so all N concurrent
	// Gets land in the same batch.
	dc := SetupInProcessClusterRF(
		t, 3, 3,
		backend.WithDistReadConsistency(backend.ConsistencyQuorum),
		backend.WithDistReadRepairBatch(200*time.Millisecond, 64),
	)

	const (
		key            = "rr-coalesce-key"
		concurrentGets = 10
	)

	owners := dc.Ring.Lookup(key)
	if len(owners) < 3 {
		t.Fatalf("expected 3 owners")
	}

	primary := dc.ByID(owners[0])
	dropped := dc.ByID(owners[1])
	reader := dc.ByID(owners[2])

	err := primary.Set(context.Background(), &cache.Item{Key: key, Value: testRepairItemValue})
	if err != nil {
		t.Fatalf("set: %v", err)
	}

	dropped.DebugDropLocal(key)

	// Drive N concurrent Gets from `reader` — every one calls
	// repairReplicas which enqueues repairs for (primary,key) and
	// (dropped,key). The coalescer collapses duplicates for the same
	// (peer,key); each subsequent enqueue past the first per (peer,
	// key) bumps ReadRepairCoalesced.
	done := make(chan struct{}, concurrentGets)
	for range concurrentGets {
		go func() {
			_, _ = reader.Get(context.Background(), key)

			done <- struct{}{}
		}()
	}

	for range concurrentGets {
		<-done
	}

	// Wait for the flush window so the dispatch counter settles.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if reader.Metrics().ReadRepairBatched > 0 {
			break
		}

		time.Sleep(10 * time.Millisecond)
	}

	coalesced := reader.Metrics().ReadRepairCoalesced
	if coalesced == 0 {
		t.Errorf("ReadRepairCoalesced: got 0, want > 0 (concurrent same-key reads should coalesce)")
	}

	// Sanity bound: at most one entry per (peer, key) survives the
	// coalesce. With 2 remote owners (primary + dropped) we expect
	// at most 2 dispatches for this key, regardless of how many Gets
	// piled in. Anything > 2 indicates a coalesce bug.
	if got := reader.Metrics().ReadRepairBatched; got > 2 {
		t.Errorf("ReadRepairBatched: got %d, want ≤ 2 (coalesce should have collapsed %d Gets)",
			got, concurrentGets)
	}
}

// TestDistReadRepair_StopDrainsBatchedQueue pins that a clean
// Stop() drains in-flight repairs before returning. Without the
// drain, queued repairs would be lost on shutdown.
//
// Uses a long flush interval so the only path that fires the
// final flush is Stop's drain — if the drain is missing or
// broken, the assertion fails because the replica never heals.
func TestDistReadRepair_StopDrainsBatchedQueue(t *testing.T) {
	t.Parallel()

	// 3/3/Quorum so the read can hit quorum with one node's local
	// copy dropped (see BatchDispatchesViaQueue for the full
	// reasoning). Long flush interval so the only path that fires
	// the final flush is Stop's drain.
	dc := SetupInProcessClusterRF(
		t, 3, 3,
		backend.WithDistReadConsistency(backend.ConsistencyQuorum),
		backend.WithDistReadRepairBatch(10*time.Second, 64), // ticker won't fire during the test
	)

	const key = "rr-drain-key"

	owners := dc.Ring.Lookup(key)
	if len(owners) < 3 {
		t.Fatalf("expected 3 owners")
	}

	primary := dc.ByID(owners[0])
	dropped := dc.ByID(owners[1])
	reader := dc.ByID(owners[2])

	err := primary.Set(context.Background(), &cache.Item{Key: key, Value: testRepairItemValue})
	if err != nil {
		t.Fatalf("set: %v", err)
	}

	dropped.DebugDropLocal(key)

	// Triggers enqueue on `reader`; the ticker won't fire for 10s.
	if _, ok := reader.Get(context.Background(), key); !ok {
		t.Fatalf("get returned not-found")
	}

	// Pre-Stop: the repair is queued but not yet dispatched.
	if dropped.LocalContains(key) {
		t.Logf("dropped node already healed before Stop (size-threshold flush?); test still valid")
	}

	// Stop must drain the queue before returning. The queue lives on
	// `reader` (the node that did the Get), so Stop is called there.
	_ = reader.Stop(context.Background())

	// Post-Stop: the dropped node should have the key (the drain
	// dispatched the queued ForwardSet).
	if !dropped.LocalContains(key) {
		t.Errorf("dropped node did not heal after Stop drain — queue drain is missing or broken")
	}
}
