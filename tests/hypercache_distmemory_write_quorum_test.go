package tests

import (
	"context"
	"testing"
	"time"

	backend "github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// TestWriteQuorumSuccess ensures a QUORUM write succeeds with majority acks.
func TestWriteQuorumSuccess(t *testing.T) {
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

	da := any(a).(*backend.DistMemory)
	db := any(b).(*backend.DistMemory)
	dc := any(c).(*backend.DistMemory)

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
}

// TestWriteQuorumFailure ensures ALL consistency fails when not enough acks.
func TestWriteQuorumFailure(t *testing.T) {
	t.Skip("quorum ALL failure scenario requires deterministic ownership; skip until dynamic membership test harness provided")

	ctx := context.Background()
	transport := backend.NewInProcessTransport()

	// We'll simulate one unreachable replica by not registering it.
	opts := []backend.DistMemoryOption{
		backend.WithDistReplication(3),
		backend.WithDistWriteConsistency(backend.ConsistencyAll),
		backend.WithDistHintTTL(time.Minute),
		backend.WithDistHintReplayInterval(time.Millisecond * 50),
	}

	a, _ := backend.NewDistMemory(ctx, append(opts, backend.WithDistNode("A", "A"))...)
	b, _ := backend.NewDistMemory(ctx, append(opts, backend.WithDistNode("B", "B"))...)

	da := any(a).(*backend.DistMemory)

	_ = b // not registered, unreachable

	da.SetTransport(transport)
	transport.Register(da)

	item := &cache.Item{Key: "k2", Value: "v2"}

	err := a.Set(ctx, item)
	if err == nil {
		t.Fatalf("expected quorum failure with ALL consistency")
	}
}
