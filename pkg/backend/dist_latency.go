package backend

import (
	"sync/atomic"
	"time"
)

// distOp represents a distributed data plane operation type.
type distOp int

const (
	opSet distOp = iota
	opGet
	opRemove
	distOpCount
)

// latencyBuckets defines fixed bucket upper bounds in nanoseconds (roughly exponential).
// Kept as a package-level var for zero-allocation hot path; suppress global lint.
//
//nolint:gochecknoglobals,mnd // bucket constants intentionally centralized
var latencyBuckets = [...]int64{
	int64(50 * time.Microsecond),
	int64(100 * time.Microsecond),
	int64(250 * time.Microsecond),
	int64(500 * time.Microsecond),
	int64(1 * time.Millisecond),
	int64(2 * time.Millisecond),
	int64(5 * time.Millisecond),
	int64(10 * time.Millisecond),
	int64(25 * time.Millisecond),
	int64(50 * time.Millisecond),
	int64(100 * time.Millisecond),
	int64(250 * time.Millisecond),
	int64(500 * time.Millisecond),
	int64(1 * time.Second),
}

// distLatencyCollector collects latency histograms (fixed buckets) for core data plane ops.
// It's intentionally lightweight: lock free, atomic per bucket.
type distLatencyCollector struct {
	// buckets[op][bucket]
	buckets [distOpCount][len(latencyBuckets) + 1]atomic.Uint64 // last bucket is +Inf
}

func newDistLatencyCollector() *distLatencyCollector { //nolint:ireturn
	return &distLatencyCollector{}
}

// observe records a duration for the given operation.
func (c *distLatencyCollector) observe(op distOp, d time.Duration) {
	ns := d.Nanoseconds()
	for i, ub := range latencyBuckets {
		if ns <= ub {
			c.buckets[op][i].Add(1)

			return
		}
	}
	// +Inf bucket
	c.buckets[op][len(latencyBuckets)].Add(1)
}

// snapshot returns a copy of bucket counts for exposure (op -> buckets slice).
func (c *distLatencyCollector) snapshot() map[string][]uint64 { //nolint:ireturn
	out := map[string][]uint64{
		"set":    make([]uint64, len(latencyBuckets)+1),
		"get":    make([]uint64, len(latencyBuckets)+1),
		"remove": make([]uint64, len(latencyBuckets)+1),
	}
	for b := range out["set"] {
		out["set"][b] = c.buckets[opSet][b].Load()
		out["get"][b] = c.buckets[opGet][b].Load()
		out["remove"][b] = c.buckets[opRemove][b].Load()
	}

	return out
}
