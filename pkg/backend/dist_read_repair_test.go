package backend

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/internal/cluster"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// captureTransport records every ForwardSet call's (peer, key,
// version) tuple so unit tests can assert "exactly these repairs
// fired". Other DistTransport methods are stubs — the queue only
// dispatches via ForwardSet, so the other verbs aren't exercised.
type captureTransport struct {
	mu    sync.Mutex
	calls []capturedSet

	// flushDelay simulates per-call latency so parallelism is
	// visible in wall-clock measurements (used by the parallel-flush
	// test). Zero disables the delay.
	flushDelay time.Duration
}

type capturedSet struct {
	peer    string
	key     string
	version uint64
}

func (c *captureTransport) ForwardSet(_ context.Context, peer string, item *cache.Item, _ bool) error {
	if c.flushDelay > 0 {
		time.Sleep(c.flushDelay)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.calls = append(c.calls, capturedSet{peer: peer, key: item.Key, version: item.Version})

	return nil
}

func (*captureTransport) ForwardGet(_ context.Context, _, _ string) (*cache.Item, bool, error) {
	return nil, false, nil
}

func (*captureTransport) ForwardRemove(_ context.Context, _, _ string, _ bool) error {
	return nil
}

func (*captureTransport) Health(_ context.Context, _ string) error            { return nil }
func (*captureTransport) IndirectHealth(_ context.Context, _, _ string) error { return nil }

func (*captureTransport) Gossip(_ context.Context, _ string, _ []GossipMember) error {
	return nil
}

func (*captureTransport) FetchMerkle(_ context.Context, _ string) (*MerkleTree, error) {
	return nil, nil //nolint:nilnil // stub never invoked by these unit tests
}

func (*captureTransport) ListKeys(_ context.Context, _, _ string) ([]string, error) {
	return nil, nil
}

func (c *captureTransport) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.calls)
}

// newQueueForTest builds a repairQueue wired to the supplied
// transport. Interval is set high enough (5s) that no tick fires
// during the test body unless the test explicitly flushes — that
// keeps the assertions deterministic.
func newQueueForTest(transport DistTransport, batchSize int) (*repairQueue, *distMetrics) {
	metrics := &distMetrics{}
	q := newRepairQueue(
		5*time.Second, // interval far enough out that tick doesn't fire mid-test
		batchSize,
		func() DistTransport { return transport },
		metrics,
		slog.New(slog.DiscardHandler),
	)

	return q, metrics
}

// TestRepairQueue_CoalesceByPeerKey pins the central contract:
// two enqueues for the same (peer, key) collapse to one entry
// (highest version wins), and ReadRepairCoalesced bumps for
// every collapsed duplicate.
func TestRepairQueue_CoalesceByPeerKey(t *testing.T) {
	t.Parallel()

	transport := &captureTransport{}
	q, metrics := newQueueForTest(transport, 100)

	peer := cluster.NodeID("peer-1")

	// First enqueue: version 1.
	q.enqueue(context.Background(), peer, &cache.Item{Key: "k1", Version: 1, Origin: "A"})

	// Second enqueue: same key, higher version → replaces, coalesce++.
	q.enqueue(context.Background(), peer, &cache.Item{Key: "k1", Version: 2, Origin: "A"})

	// Third enqueue: same key, LOWER version than current → dropped, coalesce++.
	q.enqueue(context.Background(), peer, &cache.Item{Key: "k1", Version: 1, Origin: "A"})

	// Flush and inspect what actually went on the wire.
	q.flushAll(context.Background())

	if got := transport.callCount(); got != 1 {
		t.Errorf("ForwardSet calls: got %d, want 1 (three enqueues should coalesce to one)", got)
	}

	if got := metrics.readRepairCoalesced.Load(); got != 2 {
		t.Errorf("readRepairCoalesced: got %d, want 2 (two duplicates collapsed)", got)
	}

	if got := transport.calls[0].version; got != 2 {
		t.Errorf("dispatched version: got %d, want 2 (highest of {1,2,1})", got)
	}
}

// TestRepairQueue_DistinctPeersAreIndependent pins that the queue
// keys correctly: same key to different peers are NOT coalesced.
func TestRepairQueue_DistinctPeersAreIndependent(t *testing.T) {
	t.Parallel()

	transport := &captureTransport{}
	q, metrics := newQueueForTest(transport, 100)

	q.enqueue(context.Background(), cluster.NodeID("peer-A"), &cache.Item{Key: "k1", Version: 1, Origin: "X"})
	q.enqueue(context.Background(), cluster.NodeID("peer-B"), &cache.Item{Key: "k1", Version: 1, Origin: "X"})

	q.flushAll(context.Background())

	if got := transport.callCount(); got != 2 {
		t.Errorf("ForwardSet calls: got %d, want 2 (different peers, no coalesce)", got)
	}

	if got := metrics.readRepairCoalesced.Load(); got != 0 {
		t.Errorf("readRepairCoalesced: got %d, want 0", got)
	}
}

// TestRepairQueue_FlushPeerRunsParallel pins the per-peer
// parallelism contract. Each peer's flush dispatches its entries
// in parallel via errgroup. We measure wall-clock against a stub
// transport that sleeps per-call; sequential dispatch would take
// N×delay, parallel takes ~delay.
func TestRepairQueue_FlushPeerRunsParallel(t *testing.T) {
	t.Parallel()

	const (
		entries        = 6
		perCallLatency = 50 * time.Millisecond
	)

	transport := &captureTransport{flushDelay: perCallLatency}
	q, _ := newQueueForTest(transport, 100)

	peer := cluster.NodeID("peer-1")
	for i := range entries {
		q.enqueue(context.Background(), peer, &cache.Item{Key: keyfmt(i), Version: 1, Origin: "A"})
	}

	start := time.Now()

	q.flushPeer(context.Background(), peer)

	elapsed := time.Since(start)

	// Sequential would take ≥ entries × delay = 300ms.
	// Parallel via errgroup completes in ~delay (plus scheduling slack).
	maxParallel := 3 * perCallLatency // generous ceiling for CI scheduler noise
	if elapsed > maxParallel {
		t.Errorf("flushPeer wall-clock = %v, want < %v (per-peer dispatch should be parallel)",
			elapsed, maxParallel)
	}

	if got := transport.callCount(); got != entries {
		t.Errorf("dispatched: got %d, want %d", got, entries)
	}
}

// TestRepairQueue_NilTransportIsNoop documents the defensive path:
// if the transport closure returns nil mid-flush (e.g. Stop
// landed between two flushes), the queue drops the entries without
// panicking. Merkle anti-entropy is the safety net for any repair
// that doesn't make it to the wire.
func TestRepairQueue_NilTransportIsNoop(t *testing.T) {
	t.Parallel()

	metrics := &distMetrics{}
	q := newRepairQueue(
		5*time.Second, 100,
		func() DistTransport { return nil },
		metrics,
		slog.New(slog.DiscardHandler),
	)

	q.enqueue(context.Background(), cluster.NodeID("peer-1"), &cache.Item{Key: "k1", Version: 1})

	// Should not panic.
	q.flushAll(context.Background())

	// Queue is drained even when transport is nil — the entries
	// are dropped, not retained, because the queue can't know
	// when transport will return non-nil again.
	q.mu.Lock()

	remaining := len(q.entries)
	q.mu.Unlock()

	if remaining != 0 {
		t.Errorf("entries remaining after nil-transport flush: got %d, want 0", remaining)
	}
}

// TestRepairQueue_StopDrainsPending pins the clean-shutdown
// contract: stop() doesn't return until pending entries have been
// flushed. The flusher goroutine's final flushAll runs on the
// stopCh path.
func TestRepairQueue_StopDrainsPending(t *testing.T) {
	t.Parallel()

	transport := &captureTransport{}

	metrics := &distMetrics{}
	q := newRepairQueue(
		10*time.Second, // long interval — only stop() drives the final flush
		100,
		func() DistTransport { return transport },
		metrics,
		slog.New(slog.DiscardHandler),
	)

	q.start(context.Background())

	q.enqueue(context.Background(), cluster.NodeID("peer-1"), &cache.Item{Key: "k1", Version: 1, Origin: "A"})
	q.enqueue(context.Background(), cluster.NodeID("peer-2"), &cache.Item{Key: "k2", Version: 1, Origin: "A"})

	q.stop()

	if got := transport.callCount(); got != 2 {
		t.Errorf("ForwardSet calls after stop: got %d, want 2 (stop must drain)", got)
	}
}

// TestRepairQueue_SizeThresholdFlush pins that hitting
// maxBatchSize triggers an inline flush of that peer's entries
// without waiting for the interval tick.
func TestRepairQueue_SizeThresholdFlush(t *testing.T) {
	t.Parallel()

	transport := &captureTransport{}
	q, _ := newQueueForTest(transport, 3) // tiny batch — easy to trip

	peer := cluster.NodeID("peer-1")

	q.enqueue(context.Background(), peer, &cache.Item{Key: "k1", Version: 1, Origin: "A"})
	q.enqueue(context.Background(), peer, &cache.Item{Key: "k2", Version: 1, Origin: "A"})

	if transport.callCount() != 0 {
		t.Fatalf("flush fired before threshold: %d calls", transport.callCount())
	}

	// Third enqueue trips threshold (entries map hits maxBatchSize=3).
	// The flush runs in a background goroutine; poll for completion.
	q.enqueue(context.Background(), peer, &cache.Item{Key: "k3", Version: 1, Origin: "A"})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if transport.callCount() == 3 {
			break
		}

		time.Sleep(5 * time.Millisecond)
	}

	if got := transport.callCount(); got != 3 {
		t.Errorf("ForwardSet calls: got %d, want 3 (size-threshold should have triggered flush)", got)
	}
}

// TestIsHigherVersion pins the chosen-version tie-break rule
// the coalescer uses. Mirrors repairLocalReplica's logic in
// dist_memory.go so the local and queued paths agree on which
// item wins.
func TestIsHigherVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		candidate cache.Item
		existing  cache.Item
		want      bool
	}{
		{
			"higher version wins",
			cache.Item{Version: 2, Origin: "A"},
			cache.Item{Version: 1, Origin: "A"},
			true,
		},
		{
			"lower version loses",
			cache.Item{Version: 1, Origin: "A"},
			cache.Item{Version: 2, Origin: "A"},
			false,
		},
		{
			"same version, lower origin wins",
			cache.Item{Version: 1, Origin: "A"},
			cache.Item{Version: 1, Origin: "B"},
			true,
		},
		{
			"same version, higher origin loses",
			cache.Item{Version: 1, Origin: "B"},
			cache.Item{Version: 1, Origin: "A"},
			false,
		},
		{
			"same version, same origin loses (no replace)",
			cache.Item{Version: 1, Origin: "A"},
			cache.Item{Version: 1, Origin: "A"},
			false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			candidate := tc.candidate
			existing := tc.existing

			if got := isHigherVersion(&candidate, &existing); got != tc.want {
				t.Errorf("isHigherVersion(%+v, %+v) = %v, want %v",
					candidate, existing, got, tc.want)
			}
		})
	}
}

// TestRepairQueue_ConcurrentEnqueueIsRaceFree drives many
// goroutines through enqueue + concurrent flushes; race detector
// catches any unsynchronized access. Failure manifests as a -race
// trip, not a content assertion.
func TestRepairQueue_ConcurrentEnqueueIsRaceFree(t *testing.T) {
	t.Parallel()

	transport := &captureTransport{}
	q, _ := newQueueForTest(transport, 1000)

	const (
		goroutines        = 8
		enqueuesPerWorker = 50
	)

	var wg sync.WaitGroup

	for g := range goroutines {
		gid := g

		wg.Go(func() {
			peer := cluster.NodeID("peer-" + keyfmt(gid%3))
			for i := range enqueuesPerWorker {
				q.enqueue(context.Background(), peer, &cache.Item{
					Key:     keyfmt(i),
					Version: uint64(i + 1),
					Origin:  "A",
				})
			}
		})
	}

	wg.Wait()
	q.flushAll(context.Background())

	// Sanity: at least one dispatch happened.
	if transport.callCount() == 0 {
		t.Errorf("no ForwardSet calls after concurrent enqueue (race or fixture bug)")
	}
}

// keyfmt is a tiny formatter helper so the tests aren't full of
// strconv.Itoa noise. Public-shaped so it's reusable from sibling
// tests in this package.
func keyfmt(i int) string {
	return "k" + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}

	var buf [20]byte

	pos := len(buf)
	for i > 0 {
		pos--

		buf[pos] = byte('0' + i%10)

		i /= 10
	}

	return string(buf[pos:])
}

// Compile-time guard: captureTransport must satisfy DistTransport.
var _ DistTransport = (*captureTransport)(nil)

// Compile-time check on metrics shape — if the field is removed
// the test file fails to compile, surfacing the breakage at
// `go build` rather than a runtime nil deref.
var _ = func() {
	var m distMetrics

	_ = m.readRepairBatched.Load()
	_ = m.readRepairCoalesced.Load()
	_ = atomic.Int64{}
}
