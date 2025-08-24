//go:build test

package tests

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/internal/cluster"
	backend "github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// TestHintedHandoffReplay ensures that when a replica is down during a write, a hint is queued and later replayed.
func TestHintedHandoffReplay(t *testing.T) {
	ctx := context.Background()
	transport := backend.NewInProcessTransport()

	opts := []backend.DistMemoryOption{
		backend.WithDistReplication(2),                           // primary + 1 replica
		backend.WithDistWriteConsistency(backend.ConsistencyOne), // allow local success while still attempting fanout
		backend.WithDistHintTTL(time.Minute),
		backend.WithDistHintReplayInterval(25 * time.Millisecond),
		backend.WithDistHintMaxPerNode(10),
	}

	ring := cluster.NewRing(cluster.WithReplication(2))
	m := cluster.NewMembership(ring)
	m.Upsert(cluster.NewNode("P", "P"))
	m.Upsert(cluster.NewNode("R", "R"))

	primaryOpts := append(opts, backend.WithDistNode("P", "P"), backend.WithDistMembership(m, cluster.NewNode("P", "P")))
	replicaOpts := append(opts, backend.WithDistNode("R", "R"), backend.WithDistMembership(m, cluster.NewNode("R", "R")))
	primary, _ := backend.NewDistMemory(ctx, primaryOpts...)
	replica, _ := backend.NewDistMemory(ctx, replicaOpts...)

	p := any(primary).(*backend.DistMemory)
	r := any(replica).(*backend.DistMemory)

	p.SetTransport(transport)
	// r transport deliberately not registered yet (simulate down replica)
	transport.Register(p)

	// manually start replay (constructor might have skipped due to timing)
	p.StartHintReplayForTest(ctx)

	// find a key whose owners include replica R
	key := "hint-key"
	for i := range 100 { // try a few keys
		candidate := fmt.Sprintf("hint-key-%d", i)
		owners := p.Ring().Lookup(candidate)

		foundR := false
		for _, oid := range owners {
			if string(oid) == "R" {
				foundR = true

				break
			}
		}

		if foundR {
			key = candidate

			break
		}
	}

	item := &cache.Item{Key: key, Value: "v1"}

	_ = primary.Set(ctx, item) // should attempt to replicate to R and queue hint

	if p.HintedQueueSize("R") == 0 {
		t.Fatalf("expected hint queued for unreachable replica; size=0 key=%s owners=%v", key, p.Ring().Lookup(key))
	}

	// Now register replica so hints can replay
	r.SetTransport(transport)
	transport.Register(r)
	// immediate manual replay before polling
	p.ReplayHintsForTest(ctx)

	// Wait for replay loop to deliver hint
	t.Logf("queued hints for R: %d", p.HintedQueueSize("R"))

	deadline := time.Now().Add(1 * time.Second)

	found := false
	for time.Now().Before(deadline) {
		if v, ok := replica.Get(ctx, key); ok && v != nil {
			found = true

			break
		}

		time.Sleep(25 * time.Millisecond)
	}

	if !found {
		t.Fatalf("replica did not receive hinted handoff value")
	}

	// metrics assertions for hinted handoff (at least one queued & replayed, none dropped)
	ms := p.Metrics()
	if ms.HintedQueued < 1 {
		t.Fatalf("expected HintedQueued >=1, got %d", ms.HintedQueued)
	}

	if ms.HintedReplayed < 1 {
		t.Fatalf("expected HintedReplayed >=1, got %d", ms.HintedReplayed)
	}

	if ms.HintedDropped != 0 {
		t.Fatalf("expected no HintedDropped, got %d", ms.HintedDropped)
	}
}
