package benchmark

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/pkg/backend"
)

// BenchmarkHyperCache_SetParallel exercises Set under contention.
// Phase 1b removes the redundant outer mutex around eviction-algo updates;
// this benchmark is the regression yardstick for that win.
func BenchmarkHyperCache_SetParallel(b *testing.B) {
	cache, err := hypercache.NewInMemoryWithDefaults(context.TODO(), 1_000_000)
	if err != nil {
		b.Fatal(err)
	}

	b.Cleanup(func() { _ = cache.Stop(context.TODO()) })

	var counter atomic.Uint64

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		ctx := context.Background()

		for pb.Next() {
			i := counter.Add(1)

			_ = cache.Set(ctx, "k"+strconv.FormatUint(i, 10), "v", time.Hour)
		}
	})
}

// BenchmarkHyperCache_GetParallel measures read-side throughput under contention.
// Useful as a control benchmark — Get does not take the eviction-algo mutex.
func BenchmarkHyperCache_GetParallel(b *testing.B) {
	cache, err := hypercache.NewInMemoryWithDefaults(context.TODO(), 1_000_000)
	if err != nil {
		b.Fatal(err)
	}

	b.Cleanup(func() { _ = cache.Stop(context.TODO()) })

	const preload = 1024

	for i := range preload {
		_ = cache.Set(context.TODO(), "k"+strconv.Itoa(i), "v", time.Hour)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		ctx := context.Background()

		var i int

		for pb.Next() {
			i++

			cache.Get(ctx, "k"+strconv.Itoa(i%preload))
		}
	})
}

// BenchmarkHyperCache_GetOrSetParallel exercises the path that Phase 1c rewrites
// from a goroutine-spawning fire-and-forget to a synchronous update.
func BenchmarkHyperCache_GetOrSetParallel(b *testing.B) {
	cache, err := hypercache.NewInMemoryWithDefaults(context.TODO(), 1_000_000)
	if err != nil {
		b.Fatal(err)
	}

	b.Cleanup(func() { _ = cache.Stop(context.TODO()) })

	var counter atomic.Uint64

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		ctx := context.Background()

		for pb.Next() {
			i := counter.Add(1)

			_, _ = cache.GetOrSet(ctx, "k"+strconv.FormatUint(i, 10), "v", time.Hour)
		}
	})
}

// BenchmarkHyperCache_MixedParallel simulates an 80% read / 20% write workload.
func BenchmarkHyperCache_MixedParallel(b *testing.B) {
	config, err := hypercache.NewConfig[backend.InMemory](constants.InMemoryBackend)
	if err != nil {
		b.Fatal(err)
	}

	config.HyperCacheOptions = []hypercache.Option[backend.InMemory]{
		hypercache.WithEvictionAlgorithm[backend.InMemory]("lru"),
	}
	config.InMemoryOptions = []backend.Option[backend.InMemory]{
		backend.WithCapacity[backend.InMemory](1_000_000),
	}

	cache, err := hypercache.New(context.TODO(), hypercache.GetDefaultManager(), config)
	if err != nil {
		b.Fatal(err)
	}

	b.Cleanup(func() { _ = cache.Stop(context.TODO()) })

	const preload = 4096

	for i := range preload {
		_ = cache.Set(context.TODO(), "k"+strconv.Itoa(i), "v", time.Hour)
	}

	var counter atomic.Uint64

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		ctx := context.Background()

		for pb.Next() {
			i := counter.Add(1)
			if i%5 == 0 {
				_ = cache.Set(ctx, "k"+strconv.FormatUint(i, 10), "v", time.Hour)
			} else {
				cache.Get(ctx, "k"+strconv.FormatUint(i%preload, 10))
			}
		}
	})
}
