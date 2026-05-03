package stats

import (
	"math"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/hyp3rd/hypercache/internal/constants"
)

// DefaultSampleCapacity is the per-stat ring buffer size used by
// NewHistogramStatsCollector. Bounded so memory does not grow without bound
// under sustained recording.
const DefaultSampleCapacity = 4096

// HistogramStatsCollector records statistics with per-stat atomic aggregates
// and a bounded per-stat sample window for percentile computation.
//
// Hot-path Incr/Decr/Timing/Gauge/Histogram updates use atomic counters and a
// best-effort TryLock around a fixed-size ring buffer. There is no global
// lock on the recording path; the shard map uses sync.Map for read-mostly
// lookup. Memory usage is bounded.
//
// Mean, Min, Max, Sum, and Count are exact lifetime aggregates. Median,
// Percentile, Variance, and the Values slice exposed by GetStats are
// computed over a recent-window snapshot whose size is configurable via
// NewHistogramStatsCollectorWithCapacity (default DefaultSampleCapacity).
//
// Performance characteristics: contention is now partitioned per stat key
// rather than globally serialized. A worst-case microbenchmark with all
// goroutines hammering a single stat key sees ~150 ns/op due to cache-line
// ping-pong on the shard's atomics; in realistic workloads where calls
// spread across multiple stats (cache_hits, evictions, set_count, etc.)
// throughput scales with the number of distinct keys.
type HistogramStatsCollector struct {
	stats     sync.Map // map[string]*statShard
	sampleCap int
}

// statShard holds the per-stat lock-free aggregates and the sample ring buffer.
type statShard struct {
	count atomic.Int64
	sum   atomic.Int64
	// minV/maxV track the lifetime extremes. They start at the int64 sentinels
	// MaxInt64/MinInt64; the first recorded value will CAS-update both.
	minV atomic.Int64
	maxV atomic.Int64

	samplesMu sync.Mutex
	samples   []int64 // pre-allocated to sampleCap
	pos       int     // next write index
	written   int     // number of samples actually written, capped at len(samples)
}

// NewHistogramStatsCollector creates a collector with the default sample window.
func NewHistogramStatsCollector() *HistogramStatsCollector {
	return NewHistogramStatsCollectorWithCapacity(DefaultSampleCapacity)
}

// NewHistogramStatsCollectorWithCapacity creates a collector with a custom
// per-stat sample window. capacity <= 0 falls back to DefaultSampleCapacity.
func NewHistogramStatsCollectorWithCapacity(capacity int) *HistogramStatsCollector {
	if capacity <= 0 {
		capacity = DefaultSampleCapacity
	}

	return &HistogramStatsCollector{sampleCap: capacity}
}

// Incr increments the count of a statistic by the given value.
func (c *HistogramStatsCollector) Incr(stat constants.Stat, value int64) {
	c.record(stat.String(), value)
}

// Decr decrements the count of a statistic by the given value.
func (c *HistogramStatsCollector) Decr(stat constants.Stat, value int64) {
	c.record(stat.String(), -value)
}

// Timing records the time it took for an event to occur.
func (c *HistogramStatsCollector) Timing(stat constants.Stat, value int64) {
	c.record(stat.String(), value)
}

// Gauge records the current value of a statistic.
func (c *HistogramStatsCollector) Gauge(stat constants.Stat, value int64) {
	c.record(stat.String(), value)
}

// Histogram records the statistical distribution of a set of values.
func (c *HistogramStatsCollector) Histogram(stat constants.Stat, value int64) {
	c.record(stat.String(), value)
}

// snapshotSorted returns a sorted copy of the recent sample window for shard.
// The shared backing array is never mutated; callers can sort/scan freely.
func (s *statShard) snapshotSorted() []int64 {
	s.samplesMu.Lock()

	written := s.written
	out := make([]int64, written)

	if written > 0 {
		if written < len(s.samples) {
			copy(out, s.samples[:written])
		} else {
			// ring buffer is full; reorder oldest-first so the slice is a
			// faithful temporal window (not strictly required for sort, but
			// useful for any consumer that scans Values directly).
			tail := s.samples[s.pos:]
			head := s.samples[:s.pos]

			copy(out, tail)
			copy(out[len(tail):], head)
		}
	}

	s.samplesMu.Unlock()

	if written > 0 {
		slices.Sort(out)
	}

	return out
}

// Mean returns the lifetime mean of all recorded values.
func (c *HistogramStatsCollector) Mean(stat constants.Stat) float64 {
	shard := c.shard(stat.String())

	count := shard.count.Load()
	if count == 0 {
		return 0
	}

	return float64(shard.sum.Load()) / float64(count)
}

// Median returns the median of the recent sample window.
func (c *HistogramStatsCollector) Median(stat constants.Stat) float64 {
	shard := c.shard(stat.String())

	return medianOf(shard.snapshotSorted())
}

// Percentile returns the pth percentile of the recent sample window.
// percentile must be in [0, 1]; values outside that range are clamped.
func (c *HistogramStatsCollector) Percentile(stat constants.Stat, percentile float64) float64 {
	shard := c.shard(stat.String())
	sorted := shard.snapshotSorted()

	return percentileOf(sorted, percentile)
}

// GetStats returns aggregated statistics. Mean, Min, Max, Sum, and Count are
// exact lifetime values; Median, Variance, and Values are over the recent
// sample window.
func (c *HistogramStatsCollector) GetStats() Stats {
	out := make(Stats)

	c.stats.Range(func(key, value any) bool {
		k, ok := key.(string)
		if !ok {
			return true
		}

		shard, ok := value.(*statShard)
		if !ok {
			return true
		}

		count := shard.count.Load()
		if count == 0 {
			return true
		}

		sum := shard.sum.Load()
		minVal := shard.minV.Load()
		maxVal := shard.maxV.Load()
		sorted := shard.snapshotSorted()
		mean := float64(sum) / float64(count)

		out[k] = &Stat{
			Mean:     mean,
			Median:   medianOf(sorted),
			Min:      minVal,
			Max:      maxVal,
			Values:   sorted,
			Count:    int(count),
			Sum:      sum,
			Variance: varianceOf(sorted, mean),
		}

		return true
	})

	return out
}

// shard returns (and lazily creates) the statShard for a key. Hot path uses
// sync.Map.Load to avoid the cache-line ping-pong of an RWMutex.RLock.
func (c *HistogramStatsCollector) shard(key string) *statShard {
	if existing, ok := c.stats.Load(key); ok {
		shard, ok := existing.(*statShard)
		if !ok {
			panic(errCorruptStatsMap)
		}

		return shard
	}

	fresh := &statShard{samples: make([]int64, c.sampleCap)}
	fresh.minV.Store(math.MaxInt64)
	fresh.maxV.Store(math.MinInt64)

	actual, _ := c.stats.LoadOrStore(key, fresh)

	shard, ok := actual.(*statShard)
	if !ok {
		panic(errCorruptStatsMap)
	}

	return shard
}

// errCorruptStatsMap signals a programming error: the stats sync.Map only
// stores *statShard values; a failed assertion means an unrelated writer
// has corrupted it.
const errCorruptStatsMap = "histogramcollector: stats map contains non-*statShard value"

// record is the hot path: atomic aggregate update + bounded ring write.
//
// The aggregates (count, sum, min, max) are always exact. The sample ring
// buffer is best-effort: when samplesMu is contended we TryLock and drop the
// sample silently. Under heavy concurrent recording this trades a fraction of
// percentile fidelity (samples are skipped, not blocked) for stable hot-path
// latency. Percentiles remain statistically meaningful because the dropped
// samples are uniformly distributed across recorders.
func (c *HistogramStatsCollector) record(key string, value int64) {
	shard := c.shard(key)

	shard.count.Add(1)
	shard.sum.Add(value)

	for {
		cur := shard.minV.Load()
		if value >= cur || shard.minV.CompareAndSwap(cur, value) {
			break
		}
	}

	for {
		cur := shard.maxV.Load()
		if value <= cur || shard.maxV.CompareAndSwap(cur, value) {
			break
		}
	}

	if !shard.samplesMu.TryLock() {
		return
	}

	shard.samples[shard.pos] = value
	shard.pos = (shard.pos + 1) % len(shard.samples)

	if shard.written < len(shard.samples) {
		shard.written++
	}

	shard.samplesMu.Unlock()
}

// medianOf returns the median of a sorted slice. Empty slice returns 0.
func medianOf(sorted []int64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}

	if n%2 == 1 {
		return float64(sorted[n/2])
	}

	return float64(sorted[n/2-1]+sorted[n/2]) / 2
}

// percentileOf returns the pth percentile of a sorted slice using
// nearest-rank. Empty slice returns 0; p is clamped to [0, 1].
func percentileOf(sorted []int64, percentile float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}

	switch {
	case percentile <= 0:
		return float64(sorted[0])
	case percentile >= 1:
		return float64(sorted[n-1])
	}

	idx := int(float64(n) * percentile)
	if idx >= n {
		idx = n - 1
	}

	return float64(sorted[idx])
}

// varianceOf returns the population variance of values around mean.
func varianceOf(values []int64, mean float64) float64 {
	if len(values) == 0 {
		return 0
	}

	var acc float64

	for _, value := range values {
		diff := float64(value) - mean

		acc += diff * diff
	}

	return acc / float64(len(values))
}
