package tests

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/internal/cluster"
	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// TestHintGlobalCaps ensures global hint caps (count & bytes) drop excess hints.
func TestHintGlobalCaps(t *testing.T) { //nolint:paralleltest
	ctx := context.Background()
	ring := cluster.NewRing(cluster.WithReplication(2))
	membership := cluster.NewMembership(ring)
	transport := backend.NewInProcessTransport()

	n1 := cluster.NewNode("", "n1")
	n2 := cluster.NewNode("", "n2")

	b1i, _ := backend.NewDistMemory(ctx,
		backend.WithDistMembership(membership, n1),
		backend.WithDistTransport(transport),
		backend.WithDistReplication(2),
		backend.WithDistWriteConsistency(backend.ConsistencyOne),
		backend.WithDistHintTTL(time.Minute),
		backend.WithDistHintReplayInterval(5*time.Second), // avoid replay during test
		backend.WithDistHintMaxPerNode(100),
		backend.WithDistHintMaxTotal(3), // very small global caps
		backend.WithDistHintMaxBytes(64),
	)
	b2i, _ := backend.NewDistMemory(ctx,
		backend.WithDistMembership(membership, n2),
		backend.WithDistTransport(transport),
		backend.WithDistReplication(2),
	)

	b1 := b1i.(*backend.DistMemory)
	b2 := b2i.(*backend.DistMemory)

	transport.Register(b1)
	// do not register b2 (simulate down replica so hints queue)

	// Generate many keys to force surpassing global cap (3) quickly.
	for i := range 30 {
		key := "cap-key-" + strconv.Itoa(i)

		_ = b1.Set(ctx, &cache.Item{Key: key, Value: "value-payload-xxxxxxxxxxxxxxxx"})
	}

	// allow brief time for fan-out attempts
	time.Sleep(10 * time.Millisecond)

	// Snapshot metrics
	m := b1.Metrics()
	if m.HintedQueued == 0 {
		t.Fatalf("expected some hints queued")
	}

	if m.HintedGlobalDropped == 0 {
		t.Fatalf("expected some global drops due to caps, got 0 (queued=%d)", m.HintedQueued)
	}

	if m.HintedBytes > 64 { // should respect approximate byte cap
		t.Fatalf("expected hinted bytes <=64, got %d", m.HintedBytes)
	}

	_ = b2 // silence for future extension
}
