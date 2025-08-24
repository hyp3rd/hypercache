package benchmarkdist

import (
	"context"
	"strconv"
	"testing"

	backend "github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// BenchmarkDistMemory_Set measures Set performance with replication=3 (all owners registered) under QUORUM writes.
func BenchmarkDistMemory_Set(b *testing.B) {
	ctx := context.Background()
	transport := backend.NewInProcessTransport()
	opts := []backend.DistMemoryOption{backend.WithDistReplication(3), backend.WithDistWriteConsistency(backend.ConsistencyQuorum)}

	n1, _ := backend.NewDistMemory(ctx, append(opts, backend.WithDistNode("N1", "N1"))...)
	n2, _ := backend.NewDistMemory(ctx, append(opts, backend.WithDistNode("N2", "N2"))...)
	n3, _ := backend.NewDistMemory(ctx, append(opts, backend.WithDistNode("N3", "N3"))...)

	d1 := any(n1).(*backend.DistMemory)
	d2 := any(n2).(*backend.DistMemory)
	d3 := any(n3).(*backend.DistMemory)

	d1.SetTransport(transport)
	d2.SetTransport(transport)
	d3.SetTransport(transport)
	transport.Register(d1)
	transport.Register(d2)
	transport.Register(d3)

	b.ReportAllocs()

	for i := range b.N { // standard Go benchmark loop
		it := &cache.Item{Key: "key-" + strconv.Itoa(i), Value: "v"}

		_ = n1.Set(ctx, it)
	}
}

// BenchmarkDistMemory_Get measures Get performance with replication=3 and QUORUM reads.
func BenchmarkDistMemory_Get(b *testing.B) {
	ctx := context.Background()
	transport := backend.NewInProcessTransport()
	opts := []backend.DistMemoryOption{backend.WithDistReplication(3), backend.WithDistReadConsistency(backend.ConsistencyQuorum)}

	n1, _ := backend.NewDistMemory(ctx, append(opts, backend.WithDistNode("N1", "N1"))...)
	n2, _ := backend.NewDistMemory(ctx, append(opts, backend.WithDistNode("N2", "N2"))...)
	n3, _ := backend.NewDistMemory(ctx, append(opts, backend.WithDistNode("N3", "N3"))...)

	d1 := any(n1).(*backend.DistMemory)
	d2 := any(n2).(*backend.DistMemory)
	d3 := any(n3).(*backend.DistMemory)

	d1.SetTransport(transport)
	d2.SetTransport(transport)
	d3.SetTransport(transport)
	transport.Register(d1)
	transport.Register(d2)
	transport.Register(d3)

	// seed one key to read repeatedly (avoid measuring Set cost)
	seed := &cache.Item{Key: "hot", Value: "v"}

	_ = n1.Set(ctx, seed)

	b.ReportAllocs()

	for range b.N { // standard Go benchmark loop
		_, _ = n1.Get(ctx, "hot")
	}
}
