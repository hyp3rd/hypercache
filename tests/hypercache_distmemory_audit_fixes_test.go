package tests

import (
	"context"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/internal/cluster"
	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// TestDistRemove_PromotesOnGenericForwardError pins the resilience
// contract for the Remove path symmetric to the Set path: a
// `ForwardRemove` that fails for any reason (not just the in-process
// `ErrBackendNotFound` sentinel) must promote to a local replica
// owner and apply the remove locally + fan out to peer replicas.
// Pre-fix the error was blackholed via `_ = transport.ForwardRemove(...)`,
// so Remove silently succeeded on a downed primary while leaving the
// stale value on every owner.
func TestDistRemove_PromotesOnGenericForwardError(t *testing.T) {
	t.Parallel()

	chaos := backend.NewChaos()

	dc := SetupInProcessClusterRF(
		t, 3, 3,
		backend.WithDistChaos(chaos),
		backend.WithDistWriteConsistency(backend.ConsistencyOne),
	)

	a := dc.Nodes[0]
	b := dc.Nodes[1]
	c := dc.Nodes[2]

	desired := []cluster.NodeID{b.LocalNodeID(), a.LocalNodeID(), c.LocalNodeID()}

	key, ok := FindOwnerKey(a, "remove-promote-", desired, 5000)
	if !ok {
		t.Fatalf("could not find key with owner ordering [B, A, C]")
	}

	// Seed: write the key while chaos is off so it lands on every
	// owner via the normal replication path.
	err := a.Set(context.Background(), &cache.Item{Key: key, Value: "v1"})
	if err != nil {
		t.Fatalf("seed Set: %v", err)
	}

	if !a.LocalContains(key) {
		t.Fatalf("seed: A.LocalContains is false; replication failed")
	}

	// Now block every forward and call Remove from A. The Remove
	// must NOT silently succeed; it must promote and clear the
	// local copy. The promotion counter (shared with the Set path
	// fix) bumps once.
	chaos.SetDropRate(1.0)

	rerr := a.Remove(context.Background(), key)
	if rerr != nil {
		t.Fatalf("Remove: got %v, want nil (promotion should have succeeded)", rerr)
	}

	if a.LocalContains(key) {
		t.Errorf("LocalContains(%q) after promoted Remove: got true, want false (local applyRemove didn't fire)",
			key)
	}

	if got := a.Metrics().WriteForwardPromotion; got == 0 {
		t.Errorf("WriteForwardPromotion: got 0, want > 0 (Remove promotion didn't bump the counter)")
	}
}

// TestDistHintReplay_RetainsOnGenericReplayError pins the hint-queue
// recovery contract: a replay attempt that fails with anything other
// than the in-process `ErrBackendNotFound` sentinel — including the
// network errors production transports surface — must keep the hint
// queued for a later retry, not abandon it on the first failure.
// Pre-fix the hint was dropped on `net.OpError` / `io.EOF` etc.,
// meaning a peer that was briefly unreachable during replay lost the
// queued writes entirely.
func TestDistHintReplay_RetainsOnGenericReplayError(t *testing.T) {
	t.Parallel()

	chaos := backend.NewChaos()

	dc := SetupInProcessClusterRF(
		t, 3, 3,
		backend.WithDistChaos(chaos),
		backend.WithDistWriteConsistency(backend.ConsistencyOne),
		backend.WithDistHintTTL(time.Minute),
		backend.WithDistHintReplayInterval(20*time.Millisecond),
	)

	a := dc.Nodes[0]
	b := dc.Nodes[1]
	c := dc.Nodes[2]

	desired := []cluster.NodeID{b.LocalNodeID(), a.LocalNodeID(), c.LocalNodeID()}

	key, ok := FindOwnerKey(a, "hint-retain-", desired, 5000)
	if !ok {
		t.Fatalf("could not find key with owner ordering [B, A, C]")
	}

	// Drop every forward, then write. The Set promotes locally and
	// queues a hint for the dead primary B; the replay tick fires
	// shortly after, also fails (chaos still on), and pre-fix would
	// have dropped the hint.
	chaos.SetDropRate(1.0)

	err := a.Set(context.Background(), &cache.Item{Key: key, Value: "v1"})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}

	queued := a.Metrics().HintedQueued
	if queued == 0 {
		t.Fatalf("HintedQueued: got 0, want > 0 (promotion didn't queue a hint)")
	}

	// Let the replay loop tick a few times against the still-chaotic
	// transport. Pre-fix: the very first tick drops the hint and
	// HintedReplayed stays 0 forever even after chaos clears.
	time.Sleep(150 * time.Millisecond)

	// Now heal chaos. The retained hint replays on the next tick
	// and lands on B.
	chaos.SetDropRate(0)

	if !waitForLocalContains(b, key, 2*time.Second) {
		t.Errorf("primary did not receive the write after chaos cleared (hint was dropped on the failed replay attempts instead of being retained)")
	}

	if got := a.Metrics().HintedReplayed; got == 0 {
		t.Errorf("HintedReplayed: got 0, want > 0 (hint was abandoned before chaos cleared)")
	}
}
