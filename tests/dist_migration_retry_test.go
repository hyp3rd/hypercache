//go:build test

package tests

import (
	"context"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// TestDistMigrationRetry_QueuesHintOnTransportError is the contract
// test for Phase B.2: when a forward to a replica fails for any
// transport error (not just ErrBackendNotFound), the item is queued
// onto the hint-replay queue rather than dropped.
//
// We exercise the replicateTo path on a 2-node cluster: node A writes
// a key whose replica is node B, but B is unregistered from the
// shared in-process transport. The Set's replica fan-out must enqueue
// a hint for B; replay against a re-registered B should land the key.
func TestDistMigrationRetry_QueuesHintOnTransportError(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dc := SetupInProcessClusterRF(
		t, 2, 2,
		backend.WithDistHintTTL(2*time.Second),
		backend.WithDistHintReplayInterval(50*time.Millisecond),
		backend.WithDistWriteConsistency(backend.ConsistencyOne), // tolerate partial fan-out
	)

	a, b := dc.Nodes[0], dc.Nodes[1]
	bID := string(b.LocalNodeID())

	// Find a key whose primary owner is A — only then does A's Set
	// drive replicateTo against B as a replica. When B is the primary,
	// A's Set is forwarded (or A promotes itself with no fan-out
	// because replication=2 leaves an empty replica set).
	var key string

	for i := range 64 {
		candidate := "mig-hint-" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26))

		owners := dc.Ring.Lookup(candidate)
		if len(owners) > 0 && owners[0] == a.LocalNodeID() {
			key = candidate

			break
		}
	}

	if key == "" {
		t.Fatalf("could not find a key A primaries within 64 candidates")
	}

	// Drop B from the transport — A's replica fan-out will see a
	// not-found / non-recoverable error from the transport.
	dc.Transport.Unregister(bID)

	err := a.Set(ctx, &cache.Item{
		Key:         key,
		Value:       []byte("v1"),
		Version:     1,
		Origin:      string(a.LocalNodeID()),
		LastUpdated: time.Now(),
	})
	if err != nil {
		t.Fatalf("set: %v", err)
	}

	// Hint queue must show the deferred replica write.
	if got := a.Metrics().HintedQueued; got == 0 {
		t.Fatalf("expected HintedQueued > 0 after replica unreachable, got %d", got)
	}

	// Re-register B and run hint replay synchronously — the queued
	// hint should drain and the metric for replayed-hints should
	// increment.
	dc.Transport.Register(b)
	a.ReplayHintsForTest(ctx)

	if got := a.Metrics().HintedReplayed; got == 0 {
		t.Fatalf("expected HintedReplayed > 0 after replay drained queued hint, got %d", got)
	}

	// And the key must now be present on B.
	it, ok := b.Get(ctx, key)
	if !ok || it == nil {
		t.Fatalf("expected key on B after replay, got missing")
	}
}
