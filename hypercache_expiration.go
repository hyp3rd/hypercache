package hypercache

import (
	"context"
	"time"

	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// startExpirationRoutine launches the expiration loop and listens to manual triggers and stop signals.
func (hyperCache *HyperCache[T]) startExpirationRoutine(ctx context.Context) {
	go func() {
		var tick *time.Ticker

		if hyperCache.expirationInterval > 0 {
			tick = time.NewTicker(hyperCache.expirationInterval)
		}

		for {
			if hyperCache.handleExpirationSelect(ctx, tick) { // returns true when loop should exit
				return
			}
		}
	}()
}

// handleExpirationSelect processes one select iteration; returns true if caller should exit.
func (hyperCache *HyperCache[T]) handleExpirationSelect(ctx context.Context, tick *time.Ticker) bool {
	var tickC <-chan time.Time

	if tick != nil {
		tickC = tick.C
	}

	select {
	case <-tickC:
		// scheduled expiration
		hyperCache.expirationLoop(ctx)
	case <-hyperCache.expirationTriggerCh:
		// manual/coalesced trigger
		hyperCache.expirationLoop(ctx)
		hyperCache.expirationSignalPending.Store(false)

		// drain any queued triggers quickly
		for draining := true; draining; {
			select {
			case <-hyperCache.expirationTriggerCh:
				// keep draining
			default:
				draining = false
			}
		}

	case <-hyperCache.evictCh:
		// manual eviction trigger
		hyperCache.evictionLoop(ctx)
	case <-ctx.Done():
		if tick != nil {
			tick.Stop()
		}

		return true

	case <-hyperCache.stop:
		if tick != nil {
			tick.Stop()
		}

		return true
	}

	return false
}

// execTriggerExpiration coalesces and optionally debounces expiration triggers to avoid flooding the channel.
func (hyperCache *HyperCache[T]) execTriggerExpiration() {
	// Optional debounce: if configured, drop triggers that arrive within the interval.
	if d := hyperCache.expirationDebounceInterval; d > 0 {
		last := time.Unix(0, hyperCache.lastExpirationTrigger.Load())
		if time.Since(last) < d {
			// record backpressure metric
			hyperCache.StatsCollector.Incr(constants.StatIncr, 1)

			return
		}
	}

	// Coalesce: if a signal is already pending, skip enqueueing another.
	if hyperCache.expirationSignalPending.Swap(true) {
		hyperCache.StatsCollector.Incr(constants.StatIncr, 1)

		return
	}

	select {
	case hyperCache.expirationTriggerCh <- true:
		hyperCache.lastExpirationTrigger.Store(time.Now().UnixNano())
	default:
		// channel full; keep pending flag set and record metric
		hyperCache.StatsCollector.Incr(constants.StatIncr, 1)
	}
}

// expirationLoop runs in a worker goroutine and removes expired items from the cache.
func (hyperCache *HyperCache[T]) expirationLoop(ctx context.Context) {
	hyperCache.workerPool.Enqueue(func() error {
		hyperCache.StatsCollector.Incr("expiration_loop_count", 1)
		defer hyperCache.StatsCollector.Timing("expiration_loop_duration", time.Now().UnixNano())

		var (
			expiredCount int64
			items        []*cache.Item
			err          error
		)

		// get all expired items
		items, err = hyperCache.List(ctx,
			backend.WithSortBy(constants.SortByExpiration.String()),
			backend.WithFilterFunc(func(item *cache.Item) bool {
				return item.Expiration > 0 && time.Since(item.LastAccess) > item.Expiration
			}))
		if err != nil {
			return err
		}

		// iterate all expired items and remove them
		for _, item := range items {
			expiredCount++

			err := hyperCache.Remove(ctx, item.Key)
			if err != nil {
				return err
			}

			hyperCache.itemPoolManager.Put(item)
			hyperCache.StatsCollector.Incr("item_expired_count", 1)
		}

		hyperCache.StatsCollector.Gauge("item_count", int64(hyperCache.backend.Count(ctx)))
		hyperCache.StatsCollector.Gauge("expired_item_count", expiredCount)

		return nil
	})
}

// TriggerExpiration exposes a manual expiration trigger (debounced/coalesced internally).
func (hyperCache *HyperCache[T]) TriggerExpiration() { hyperCache.execTriggerExpiration() }
