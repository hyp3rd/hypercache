package stats

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hyp3rd/hypercache/internal/constants"
)

func TestHistogramStatsCollector_BasicAggregates(t *testing.T) {
	t.Parallel()

	c := NewHistogramStatsCollector()

	c.Incr(constants.StatIncr, 10)
	c.Incr(constants.StatIncr, 20)
	c.Incr(constants.StatIncr, 30)

	if got := c.Mean(constants.StatIncr); got != 20 {
		t.Errorf("Mean = %v, want 20", got)
	}

	allStats := c.GetStats()

	stat, ok := allStats[constants.StatIncr.String()]
	if !ok {
		t.Fatalf("missing stat key")
	}

	if stat.Min != 10 {
		t.Errorf("Min = %d, want 10", stat.Min)
	}

	if stat.Max != 30 {
		t.Errorf("Max = %d, want 30", stat.Max)
	}

	if stat.Sum != 60 {
		t.Errorf("Sum = %d, want 60", stat.Sum)
	}

	if stat.Count != 3 {
		t.Errorf("Count = %d, want 3", stat.Count)
	}

	if stat.Mean != 20 {
		t.Errorf("Mean = %v, want 20", stat.Mean)
	}
}

func TestHistogramStatsCollector_DecrStoresNegative(t *testing.T) {
	t.Parallel()

	c := NewHistogramStatsCollector()

	c.Incr(constants.StatIncr, 5)
	c.Decr(constants.StatIncr, 2) // stored as -2

	stat := c.GetStats()[constants.StatIncr.String()]
	if stat.Sum != 3 {
		t.Errorf("Sum = %d, want 3", stat.Sum)
	}

	if stat.Min != -2 {
		t.Errorf("Min = %d, want -2", stat.Min)
	}

	if stat.Max != 5 {
		t.Errorf("Max = %d, want 5", stat.Max)
	}
}

func TestHistogramStatsCollector_Median(t *testing.T) {
	t.Parallel()

	c := NewHistogramStatsCollector()

	for _, v := range []int64{3, 1, 4, 1, 5, 9, 2, 6} {
		c.Histogram(constants.StatHistogram, v)
	}

	// sorted: 1,1,2,3,4,5,6,9 -> median = (3+4)/2 = 3.5
	if got := c.Median(constants.StatHistogram); got != 3.5 {
		t.Errorf("Median = %v, want 3.5", got)
	}

	c.Histogram(constants.StatHistogram, 7)

	// sorted (9 elems): 1,1,2,3,4,5,6,7,9 -> median = 4
	if got := c.Median(constants.StatHistogram); got != 4 {
		t.Errorf("Median (odd n) = %v, want 4", got)
	}
}

func TestHistogramStatsCollector_Percentile(t *testing.T) {
	t.Parallel()

	c := NewHistogramStatsCollector()

	for i := int64(1); i <= 100; i++ {
		c.Histogram(constants.StatHistogram, i)
	}

	cases := []struct {
		percentile float64
		want       float64
	}{
		{0, 1},
		{0.5, 51}, // index = 50, sorted[50] = 51
		{0.99, 100},
		{1, 100},
	}

	for _, tc := range cases {
		if got := c.Percentile(constants.StatHistogram, tc.percentile); got != tc.want {
			t.Errorf("Percentile(%v) = %v, want %v", tc.percentile, got, tc.want)
		}
	}
}

func TestHistogramStatsCollector_EmptyStat(t *testing.T) {
	t.Parallel()

	c := NewHistogramStatsCollector()

	// querying an unrecorded stat must return zero values, not panic
	if got := c.Mean(constants.StatIncr); got != 0 {
		t.Errorf("Mean(empty) = %v, want 0", got)
	}

	if got := c.Median(constants.StatIncr); got != 0 {
		t.Errorf("Median(empty) = %v, want 0", got)
	}

	if got := c.Percentile(constants.StatIncr, 0.5); got != 0 {
		t.Errorf("Percentile(empty) = %v, want 0", got)
	}

	if len(c.GetStats()) != 0 {
		t.Errorf("GetStats(empty) returned non-empty result")
	}
}

func TestHistogramStatsCollector_BoundedSamples(t *testing.T) {
	t.Parallel()

	const windowCap = 8

	c := NewHistogramStatsCollectorWithCapacity(windowCap)

	// Write more than windowCap samples; sample buffer must stay at windowCap.
	for i := range int64(100) {
		c.Histogram(constants.StatHistogram, i)
	}

	stat := c.GetStats()[constants.StatHistogram.String()]
	if stat == nil {
		t.Fatalf("missing stat")
	}

	if stat.Count != 100 {
		t.Errorf("lifetime Count = %d, want 100", stat.Count)
	}

	if got := len(stat.Values); got != windowCap {
		t.Errorf("len(Values) = %d, want %d (bounded)", got, windowCap)
	}

	// Min/Max must still reflect the lifetime range, not just the window.
	if stat.Min != 0 {
		t.Errorf("lifetime Min = %d, want 0", stat.Min)
	}

	if stat.Max != 99 {
		t.Errorf("lifetime Max = %d, want 99", stat.Max)
	}
}

// TestHistogramStatsCollector_ConcurrentRecord is the race-detector regression
// test. Concurrent recorders + a concurrent reader of GetStats/Mean must not
// trip the race detector (which the previous implementation would, since it
// sorted shared backing arrays in-place under RLock).
func TestHistogramStatsCollector_ConcurrentRecord(t *testing.T) {
	t.Parallel()

	c := NewHistogramStatsCollector()

	const writers = 8

	const perWriter = 5_000

	var writersWG sync.WaitGroup

	for w := range writers {
		writersWG.Go(func() {
			for i := range perWriter {
				c.Incr(constants.StatIncr, int64(w*perWriter+i))
			}
		})
	}

	// Reader runs until writers finish; signaled via stop channel.
	stop := make(chan struct{})

	var readerWG sync.WaitGroup

	readerWG.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				_ = c.GetStats()
				_ = c.Mean(constants.StatIncr)
				_ = c.Percentile(constants.StatIncr, 0.99)
			}
		}
	})

	writersWG.Wait()
	close(stop)
	readerWG.Wait()

	stat := c.GetStats()[constants.StatIncr.String()]
	if stat == nil {
		t.Fatalf("missing stat after concurrent recording")
	}

	wantCount := int64(writers * perWriter)
	if int64(stat.Count) != wantCount {
		t.Errorf("Count = %d, want %d", stat.Count, wantCount)
	}
}

// TestHistogramStatsCollector_GetStatsSnapshotIsolated ensures GetStats does
// not mutate the live sample buffer. The previous implementation called
// slices.Sort on the live buffer, racing with concurrent writers.
func TestHistogramStatsCollector_GetStatsSnapshotIsolated(t *testing.T) {
	t.Parallel()

	c := NewHistogramStatsCollector()

	for i := range int64(32) {
		c.Histogram(constants.StatHistogram, i)
	}

	first := c.GetStats()[constants.StatHistogram.String()]
	firstValues := append([]int64(nil), first.Values...)

	// mutate the returned slice; the next snapshot must be unaffected.
	for i := range first.Values {
		first.Values[i] = -999
	}

	second := c.GetStats()[constants.StatHistogram.String()]
	for i, v := range second.Values {
		if v != firstValues[i] {
			t.Errorf("GetStats returned shared slice: idx %d = %d, want %d", i, v, firstValues[i])

			break
		}
	}
}

// TestHistogramStatsCollector_NoMemoryLeak verifies the bounded sample window
// keeps memory usage flat under sustained recording. The previous
// implementation appended forever and would grow unbounded.
func TestHistogramStatsCollector_NoMemoryLeak(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping memory soak in short mode")
	}

	const windowCap = 1024

	c := NewHistogramStatsCollectorWithCapacity(windowCap)

	// Prime the ring buffer.
	for i := range windowCap {
		c.Histogram(constants.StatHistogram, int64(i))
	}

	runtime.GC() //nolint:revive // intentional GC to take a clean heap reading for the leak assertion

	var before runtime.MemStats

	runtime.ReadMemStats(&before)

	// Record 1M more samples; ring buffer is full so allocations should be ~0.
	const extra = 1_000_000

	for i := range extra {
		c.Histogram(constants.StatHistogram, int64(i))
	}

	runtime.GC() //nolint:revive // intentional GC to take a clean heap reading for the leak assertion

	var after runtime.MemStats

	runtime.ReadMemStats(&after)

	// Heap should not grow by more than a small constant (allow 1MB slack for
	// runtime overhead, GC bookkeeping, etc.).
	const slack = 1 << 20

	growth := int64(after.HeapAlloc) - int64(before.HeapAlloc) //nolint:gosec // HeapAlloc never approaches int64 max in this test
	if growth > slack {
		t.Errorf("heap grew by %d bytes during steady-state recording (want <= %d)", growth, slack)
	}

	// And lifetime count is still exact.
	stat := c.GetStats()[constants.StatHistogram.String()]
	want := int64(windowCap + extra)

	if int64(stat.Count) != want {
		t.Errorf("Count = %d, want %d", stat.Count, want)
	}
}

// TestHistogramStatsCollector_AtomicMinMaxRace exercises the CAS loops in
// record() under concurrent extreme values.
func TestHistogramStatsCollector_AtomicMinMaxRace(t *testing.T) {
	t.Parallel()

	c := NewHistogramStatsCollector()

	var wg sync.WaitGroup

	const goroutines = 16

	const perGoroutine = 1000

	var seen atomic.Int64

	for g := range goroutines {
		wg.Go(func() {
			for i := range perGoroutine {
				value := int64(g*perGoroutine + i)
				c.Incr(constants.StatIncr, value)
				seen.Add(1)
			}
		})
	}

	wg.Wait()

	stat := c.GetStats()[constants.StatIncr.String()]
	if stat == nil {
		t.Fatalf("missing stat")
	}

	if stat.Min != 0 {
		t.Errorf("Min = %d, want 0", stat.Min)
	}

	if stat.Max != int64(goroutines*perGoroutine-1) {
		t.Errorf("Max = %d, want %d", stat.Max, goroutines*perGoroutine-1)
	}

	if int64(stat.Count) != seen.Load() {
		t.Errorf("Count = %d, want %d", stat.Count, seen.Load())
	}
}
