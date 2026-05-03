package tests

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/internal/cluster"
	"github.com/hyp3rd/hypercache/internal/sentinel"
	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// TestWriteQuorumSuccess ensures a QUORUM write succeeds with majority acks.
func TestWriteQuorumSuccess(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	transport := backend.NewInProcessTransport()

	// replication=3, write consistency QUORUM
	opts := []backend.DistMemoryOption{
		backend.WithDistReplication(2),
		backend.WithDistWriteConsistency(backend.ConsistencyQuorum),
	}

	a, _ := backend.NewDistMemory(ctx, append(opts, backend.WithDistNode("A", "A"))...)
	b, _ := backend.NewDistMemory(ctx, append(opts, backend.WithDistNode("B", "B"))...)
	c, _ := backend.NewDistMemory(ctx, append(opts, backend.WithDistNode("C", "C"))...)

	da, ok := any(a).(*backend.DistMemory)
	if !ok {
		t.Fatalf("expected *backend.DistMemory, got %T", a)
	}

	db, ok := any(b).(*backend.DistMemory)
	if !ok {
		t.Fatalf("expected *backend.DistMemory, got %T", b)
	}

	dc, ok := any(c).(*backend.DistMemory)
	if !ok {
		t.Fatalf("expected *backend.DistMemory, got %T", c)
	}

	StopOnCleanup(t, da)
	StopOnCleanup(t, db)
	StopOnCleanup(t, dc)

	da.SetTransport(transport)
	db.SetTransport(transport)
	dc.SetTransport(transport)
	transport.Register(da)
	transport.Register(db)
	transport.Register(dc)

	item := &cache.Item{Key: "k1", Value: "v1"}

	err := a.Set(ctx, item)
	if err != nil { // should succeed with quorum (all up)
		t.Fatalf("expected success, got %v", err)
	}

	// metrics assertions (writeAttempts >=1, writeQuorumFailures stays 0)
	metrics := da.Metrics()
	if metrics.WriteAttempts < 1 {
		t.Fatalf("expected WriteAttempts >=1, got %d", metrics.WriteAttempts)
	}

	if metrics.WriteQuorumFailures != 0 {
		t.Fatalf("unexpected WriteQuorumFailures: %d", metrics.WriteQuorumFailures)
	}
}

// TestWriteQuorumFailure ensures ALL consistency fails when not enough acks.
func TestWriteQuorumFailure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	transport := backend.NewInProcessTransport()

	// Shared ring/membership so ownership is identical across nodes.
	ring := cluster.NewRing(cluster.WithReplication(3))
	m := cluster.NewMembership(ring)
	m.Upsert(cluster.NewNode("A", "A"))
	m.Upsert(cluster.NewNode("B", "B"))
	m.Upsert(cluster.NewNode("C", "C"))

	opts := []backend.DistMemoryOption{
		backend.WithDistReplication(3),
		backend.WithDistWriteConsistency(backend.ConsistencyAll),
		backend.WithDistHintTTL(time.Minute),
		backend.WithDistHintReplayInterval(50 * time.Millisecond),
	}

	// Create three nodes but only register two with transport to force ALL failure.
	na, _ := backend.NewDistMemory(
		ctx,
		append(opts, backend.WithDistNode("A", "A"), backend.WithDistMembership(m, cluster.NewNode("A", "A")))...)
	nb, _ := backend.NewDistMemory(
		ctx,
		append(opts, backend.WithDistNode("B", "B"), backend.WithDistMembership(m, cluster.NewNode("B", "B")))...)

	nc, _ := backend.NewDistMemory(
		ctx,
		append(opts, backend.WithDistNode("C", "C"), backend.WithDistMembership(m, cluster.NewNode("C", "C")))...)

	da, ok := any(na).(*backend.DistMemory)
	if !ok {
		t.Fatalf("expected *backend.DistMemory, got %T", na)
	}

	db, ok := any(nb).(*backend.DistMemory)
	if !ok {
		t.Fatalf("expected *backend.DistMemory, got %T", nb)
	}

	dc, ok := any(nc).(*backend.DistMemory)
	if !ok {
		t.Fatalf("expected *backend.DistMemory, got %T", nc)
	}

	StopOnCleanup(t, da)
	StopOnCleanup(t, db)
	StopOnCleanup(t, dc)

	da.SetTransport(transport)
	db.SetTransport(transport)
	transport.Register(da)
	transport.Register(db) // C intentionally left unregistered to simulate it being offline

	// Find a key whose owners include all three nodes (replication=3 ensures this) – just brute force until order stable.
	key := "quorum-all-fail"
	for i := range 50 { // try some keys to ensure A is primary sometimes; not strictly required
		candidate := fmt.Sprintf("quorum-all-fail-%d", i)

		owners := da.Ring().Lookup(candidate)
		if len(owners) == 3 && string(owners[0]) == "A" { // prefer A primary for clarity
			key = candidate

			break
		}
	}

	item := &cache.Item{Key: key, Value: "v-fail"}

	err := na.Set(ctx, item)
	if !errors.Is(err, sentinel.ErrQuorumFailed) {
		// Provide ring owners for debugging.
		owners := da.Ring().Lookup(key)

		ids := make([]string, 0, len(owners))
		for _, o := range owners {
			ids = append(ids, string(o))
		}

		t.Fatalf("expected ErrQuorumFailed, got %v (owners=%v)", err, ids)
	}

	metrics := da.Metrics()
	if metrics.WriteQuorumFailures < 1 {
		t.Fatalf("expected WriteQuorumFailures >=1, got %d", metrics.WriteQuorumFailures)
	}

	if metrics.WriteAttempts < 1 { // should have attempted at least once
		t.Fatalf("expected WriteAttempts >=1, got %d", metrics.WriteAttempts)
	}
}
