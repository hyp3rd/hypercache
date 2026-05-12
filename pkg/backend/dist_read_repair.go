package backend

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/hyp3rd/hypercache/internal/cluster"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// repairQueue is the opt-in async coalescer for read-repair fan-out.
// When configured via WithDistReadRepairBatch, repairRemoteReplica
// enqueues into this queue instead of dispatching synchronously;
// a background flusher groups pending repairs by destination peer
// and dispatches them on the configured interval (or as soon as a
// per-peer batch reaches maxBatchSize).
//
// Coalescing rule: per (peer, key), only the highest-version item
// is kept. Concurrent reads of the same hot key produce one repair,
// not N — the second enqueue sees the first's entry and replaces
// it iff the new version is higher, otherwise short-circuits and
// bumps ReadRepairCoalesced.
//
// Lifecycle: started by NewDistMemory when the option is set;
// drained on Stop() so in-flight repairs aren't dropped on a clean
// shutdown. Crash exit loses queued repairs by design — merkle
// anti-entropy is the safety net for any persistence-grade
// convergence guarantee.
type repairQueue struct {
	interval     time.Duration
	maxBatchSize int

	mu      sync.Mutex
	entries map[cluster.NodeID]map[string]*cache.Item

	transport func() DistTransport
	metrics   *distMetrics
	logger    *slog.Logger

	stopCh chan struct{}
	doneCh chan struct{}
}

// newRepairQueue constructs the queue. interval > 0 and
// maxBatchSize > 0 are caller invariants; the option layer enforces
// these so the queue itself stays focused on the dispatch logic.
func newRepairQueue(
	interval time.Duration,
	maxBatchSize int,
	transport func() DistTransport,
	metrics *distMetrics,
	logger *slog.Logger,
) *repairQueue {
	return &repairQueue{
		interval:     interval,
		maxBatchSize: maxBatchSize,
		entries:      map[cluster.NodeID]map[string]*cache.Item{},
		transport:    transport,
		metrics:      metrics,
		logger:       logger,
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
	}
}

// start launches the background flusher. Idempotent guard is the
// caller's responsibility (newRepairQueue + start happen exactly
// once from NewDistMemory).
func (q *repairQueue) start(ctx context.Context) {
	go q.run(ctx)
}

// stop signals shutdown, waits for the flusher to drain remaining
// entries, then returns. Safe to call multiple times — repeated
// closes on stopCh are guarded by the channel's already-closed
// state via a select.
func (q *repairQueue) stop() {
	select {
	case <-q.stopCh:
		// already closed
	default:
		close(q.stopCh)
	}

	<-q.doneCh
}

// enqueue records a pending repair for (peer, item.Key). If a
// higher-or-equal-version entry already exists for that key on
// that peer, the new entry is dropped and ReadRepairCoalesced is
// bumped. If the new entry replaces a lower-version one, the
// counter is also bumped (a redundant repair was just collapsed).
//
// Triggers an inline flush of the peer's batch if it reaches
// maxBatchSize. The inline flush runs in its own goroutine so
// enqueue returns immediately to the read path. The detached
// goroutine uses context.WithoutCancel(ctx) so it inherits tracing
// values from the caller without being cancelled when the request
// scope ends (this is fire-and-forget by design — the read path
// has already returned by the time the flush completes).
func (q *repairQueue) enqueue(ctx context.Context, peer cluster.NodeID, item *cache.Item) {
	if item == nil {
		return
	}

	q.mu.Lock()

	peerEntries := q.entries[peer]
	if peerEntries == nil {
		peerEntries = map[string]*cache.Item{}
		q.entries[peer] = peerEntries
	}

	existing, found := peerEntries[item.Key]
	if found && !isHigherVersion(item, existing) {
		// Existing entry is at-least-as-new; new repair is redundant.
		q.metrics.readRepairCoalesced.Add(1)
		q.mu.Unlock()

		return
	}

	if found {
		// Replacing a lower-version entry — still a coalesce (a
		// redundant wire call is being saved).
		q.metrics.readRepairCoalesced.Add(1)
	}

	cloned := *item

	peerEntries[item.Key] = &cloned

	shouldFlush := len(peerEntries) >= q.maxBatchSize
	q.mu.Unlock()

	if shouldFlush {
		flushCtx := context.WithoutCancel(ctx)

		go q.flushPeer(flushCtx, peer)
	}
}

// run is the flusher goroutine body. Ticks on interval, flushes
// everything; on stopCh signal, does one final flushAll, then
// closes doneCh so stop() can return. Shutdown flushes use
// context.WithoutCancel so the drain dispatches even when the
// parent ctx is already Done — losing queued repairs on a clean
// stop would defeat the drain guarantee.
func (q *repairQueue) run(ctx context.Context) {
	defer close(q.doneCh)

	ticker := time.NewTicker(q.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			q.flushAll(context.WithoutCancel(ctx))

			return

		case <-q.stopCh:
			q.flushAll(context.WithoutCancel(ctx))

			return

		case <-ticker.C:
			q.flushAll(ctx)
		}
	}
}

// flushAll drains every peer's pending entries. Used by the
// ticker path and by stop(). Per-peer flushes run sequentially
// here; per-peer parallelism happens INSIDE flushPeer via errgroup.
func (q *repairQueue) flushAll(ctx context.Context) {
	q.mu.Lock()

	peers := make([]cluster.NodeID, 0, len(q.entries))
	for peer := range q.entries {
		peers = append(peers, peer)
	}

	q.mu.Unlock()

	for _, peer := range peers {
		q.flushPeer(ctx, peer)
	}
}

// flushPeer drains one peer's pending entries and dispatches them
// in parallel via ForwardSet calls. Per-call failures are absorbed
// — read-repair is best-effort, merkle anti-entropy carries the
// convergence guarantee.
//
// Detaches the slice under the lock then releases before doing
// network I/O so enqueue() callers don't block on the wire.
func (q *repairQueue) flushPeer(ctx context.Context, peer cluster.NodeID) {
	q.mu.Lock()

	peerEntries := q.entries[peer]
	if len(peerEntries) == 0 {
		q.mu.Unlock()

		return
	}

	delete(q.entries, peer)
	q.mu.Unlock()

	transport := q.transport()
	if transport == nil {
		// Transport not configured (or torn down mid-flush). The
		// repairs are dropped; merkle anti-entropy will catch up.
		return
	}

	items := make([]*cache.Item, 0, len(peerEntries))
	for _, it := range peerEntries {
		items = append(items, it)
	}

	var g errgroup.Group

	for _, it := range items {
		g.Go(func() error {
			err := transport.ForwardSet(ctx, string(peer), it, false)
			if err != nil {
				// Best-effort — log at Debug, count toward the
				// aggregate readRepair budget anyway since the
				// receiver might still apply some of them. Failures
				// don't propagate to enqueue callers; the read path
				// has already returned.
				if q.logger != nil {
					q.logger.Debug(
						"read-repair flush: ForwardSet failed",
						slog.String("peer", string(peer)),
						slog.String("key", it.Key),
						slog.Any("err", err),
					)
				}
			}

			q.metrics.readRepairBatched.Add(1)

			return nil
		})
	}

	_ = g.Wait()
}

// isHigherVersion reports whether `candidate` should replace
// `existing` in the queue. Mirrors the chosen-version rule used
// by repairLocalReplica (dist_memory.go:3508) — strict version
// comparison, ties broken by origin string. Same rule on both
// the local-repair and queued-repair sides keeps the convergence
// semantics consistent.
func isHigherVersion(candidate, existing *cache.Item) bool {
	if candidate.Version > existing.Version {
		return true
	}

	if candidate.Version < existing.Version {
		return false
	}

	// Versions equal: tie-break by origin (lower origin string wins,
	// matching repairLocalReplica's `localIt.Origin > chosen.Origin`
	// condition).
	return candidate.Origin < existing.Origin
}
