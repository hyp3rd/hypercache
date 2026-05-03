package v2

import (
	"strconv"
	"sync"
	"testing"
)

// BenchmarkConcurrentMap_Count is the regression yardstick for Phase 2a
// (per-shard atomic counter). Today Count() acquires 32 shard RLocks
// sequentially; after Phase 2a it should sum 32 atomics with no locks.
func BenchmarkConcurrentMap_Count(b *testing.B) {
	cm := New()
	for i := range 4096 {
		cm.Set("k"+strconv.Itoa(i), &Item{Key: "k" + strconv.Itoa(i)})
	}

	b.ResetTimer()

	for b.Loop() {
		_ = cm.Count()
	}
}

// BenchmarkConcurrentMap_CountParallel exposes the Count() lock storm
// when called concurrently with writers — the realistic eviction-loop scenario.
func BenchmarkConcurrentMap_CountParallel(b *testing.B) {
	cm := New()
	for i := range 4096 {
		cm.Set("k"+strconv.Itoa(i), &Item{Key: "k" + strconv.Itoa(i)})
	}

	stop := make(chan struct{})

	var wg sync.WaitGroup

	wg.Go(func() {
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
				cm.Set("w"+strconv.Itoa(i), &Item{Key: "w" + strconv.Itoa(i)})

				i++
			}
		}
	})

	b.ResetTimer()

	for b.Loop() {
		_ = cm.Count()
	}

	b.StopTimer()
	close(stop)
	wg.Wait()
}

// BenchmarkConcurrentMap_GetShard measures the cost of the in-process shard hash.
// Phase 2c switches from inlined FNV-1a to xxhash.Sum64String for one canonical hash.
func BenchmarkConcurrentMap_GetShard(b *testing.B) {
	cm := New()
	keys := make([]string, 1024)

	for i := range keys {
		keys[i] = "some-cache-key-" + strconv.Itoa(i)
	}

	b.ResetTimer()

	var i int

	for b.Loop() {
		_ = cm.GetShard(keys[i&1023])
		i++
	}
}

// BenchmarkConcurrentMap_IterBuffered measures allocation pressure of the
// channel-based iterator. Phase 2b replaces it with iter.Seq2 — this benchmark
// proves the alloc/op drops to ~0.
func BenchmarkConcurrentMap_IterBuffered(b *testing.B) {
	cm := New()
	for i := range 4096 {
		cm.Set("k"+strconv.Itoa(i), &Item{Key: "k" + strconv.Itoa(i)})
	}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		for tup := range cm.IterBuffered() {
			_ = tup
		}
	}
}
