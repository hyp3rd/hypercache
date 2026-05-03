package stats

import (
	"testing"

	"github.com/hyp3rd/hypercache/internal/constants"
)

// BenchmarkHistogramIncr is the single-goroutine baseline for Phase 1a.
// Today this serializes on a global Mutex and appends to an unbounded slice.
func BenchmarkHistogramIncr(b *testing.B) {
	c := NewHistogramStatsCollector()

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		c.Incr(constants.StatIncr, 1)
	}
}

// BenchmarkHistogramIncrParallel is the contention regression yardstick.
// Phase 1a should turn this into a lock-free atomic add; expect ≥10x.
func BenchmarkHistogramIncrParallel(b *testing.B) {
	c := NewHistogramStatsCollector()

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Incr(constants.StatIncr, 1)
		}
	})
}

// BenchmarkHistogramTimingParallel exercises the same hot path with a different stat key.
func BenchmarkHistogramTimingParallel(b *testing.B) {
	c := NewHistogramStatsCollector()

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Timing(constants.StatTiming, 1)
		}
	})
}

// BenchmarkHistogramRealisticParallel models a realistic workload where many
// stat keys spread the contention. The single-key Parallel benchmark above is
// the synthetic worst case; this benchmark is what production looks like.
func BenchmarkHistogramRealisticParallel(b *testing.B) {
	keys := []constants.Stat{
		constants.StatIncr,
		constants.StatDecr,
		constants.StatTiming,
		constants.StatGauge,
		constants.StatHistogram,
	}

	c := NewHistogramStatsCollector()

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		var i int

		for pb.Next() {
			c.Incr(keys[i%len(keys)], 1)

			i++
		}
	})
}

// BenchmarkHistogramGetStats measures the read path. Currently sorts the
// backing slice in-place under RLock (race) and re-sorts per-stat multiple times.
func BenchmarkHistogramGetStats(b *testing.B) {
	c := NewHistogramStatsCollector()
	for range 4096 {
		c.Incr(constants.StatIncr, 1)
		c.Timing(constants.StatTiming, 5)
		c.Gauge(constants.StatGauge, 7)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		_ = c.GetStats()
	}
}
