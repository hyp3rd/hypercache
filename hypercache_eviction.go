package hypercache

import (
	"context"
	"time"

	"github.com/hyp3rd/sectools/pkg/converters"

	"github.com/hyp3rd/hypercache/pkg/backend"
	"github.com/hyp3rd/hypercache/pkg/eviction"
)

// configureEvictionSettings computes derived eviction settings like shouldEvict and default maxEvictionCount.
func configureEvictionSettings[T backend.IBackendConstrain](hc *HyperCache[T]) {
	hc.shouldEvict.Store(hc.evictionInterval == 0 && hc.backend.Capacity() > 0)

	if hc.maxEvictionCount == 0 {
		maxEvictionCount, err := converters.ToUint(hc.backend.Capacity())
		if err != nil {
			hc.maxEvictionCount = 1

			return
		}

		hc.maxEvictionCount = maxEvictionCount
	}
}

// initEvictionAlgorithm initializes the eviction algorithm for the cache.
//
// When evictionShardCount > 1 (default 32) the algorithm is wrapped in
// eviction.Sharded — same hash as ConcurrentMap, so a key's data shard and
// eviction shard align. This eliminates the global eviction-algorithm mutex
// at the cost of strict global LRU/LFU ordering. shardCount <= 1 keeps the
// previous single-instance behavior.
func initEvictionAlgorithm[T backend.IBackendConstrain](hc *HyperCache[T]) error {
	maxEvictionCount, err := converters.ToInt(hc.maxEvictionCount)
	if err != nil {
		return err
	}

	algorithmName := hc.evictionAlgorithmName
	if algorithmName == "" {
		algorithmName = "lru"
	}

	if hc.evictionShardCount > 1 {
		hc.evictionAlgorithm, err = eviction.NewSharded(algorithmName, maxEvictionCount, hc.evictionShardCount)

		return err
	}

	hc.evictionAlgorithm, err = eviction.NewEvictionAlgorithm(algorithmName, maxEvictionCount)

	return err
}

// startEvictionRoutine launches the periodic eviction loop if configured.
func (hyperCache *HyperCache[T]) startEvictionRoutine(ctx context.Context) {
	if hyperCache.evictionInterval <= 0 {
		return
	}

	tick := time.NewTicker(hyperCache.evictionInterval)

	go func() {
		for {
			select {
			case <-tick.C:
				hyperCache.evictionLoop(ctx)
			case <-ctx.Done():
				tick.Stop()

				return

			case <-hyperCache.stop:
				tick.Stop()

				return
			}
		}
	}()
}

// evictionLoop runs in a worker goroutine and evicts items until the backend is at or below capacity,
// or the configured maxEvictionCount is reached. The eviction policy is determined by the configured
// algorithm.
func (hyperCache *HyperCache[T]) evictionLoop(ctx context.Context) {
	// Enqueue the eviction loop in the worker pool to avoid blocking the main goroutine if the eviction loop is slow
	hyperCache.workerPool.Enqueue(func() error {
		hyperCache.StatsCollector.Incr("eviction_loop_count", 1)
		defer hyperCache.StatsCollector.Timing("eviction_loop_duration", time.Now().UnixNano())

		var evictedCount uint

		for hyperCache.backend.Count(ctx) > hyperCache.backend.Capacity() {
			if hyperCache.maxEvictionCount == evictedCount {
				break
			}

			// Each eviction algorithm provides its own internal mutex; no
			// outer lock needed. The Evict() -> Remove() window below is
			// already non-atomic by design (other workers may insert items
			// between the two calls); a wider lock would not change that.
			key, ok := hyperCache.evictionAlgorithm.Evict()
			if !ok {
				// no more items to evict
				break
			}

			// remove the item from the cache
			err := hyperCache.Remove(ctx, key)
			if err != nil {
				return err
			}

			evictedCount++

			hyperCache.StatsCollector.Incr("item_evicted_count", 1)
		}

		itemCount, err := converters.ToInt64(hyperCache.backend.Count(ctx))
		if err != nil {
			return err
		}

		hyperCache.StatsCollector.Gauge("item_count", itemCount)

		evictedCount64, err := converters.ToInt64(evictedCount)
		if err != nil {
			return err
		}

		hyperCache.StatsCollector.Gauge("evicted_item_count", evictedCount64)

		return nil
	})
}

// evictItem is a helper function that removes an item from the cache and returns the key of the evicted item.
// If no item can be evicted, it returns a false.
func (hyperCache *HyperCache[T]) evictItem(ctx context.Context) (string, bool) {
	key, ok := hyperCache.evictionAlgorithm.Evict()
	if !ok {
		// no more items to evict
		return "", false
	}

	err := hyperCache.Remove(ctx, key)
	if err != nil {
		return "", false
	}

	return key, true
}

// SetCapacity sets the capacity of the cache. If the new capacity is smaller than the current number of items in the cache,
// it evicts the excess items from the cache.
func (hyperCache *HyperCache[T]) SetCapacity(ctx context.Context, capacity int) {
	// set capacity of the backend
	hyperCache.backend.SetCapacity(capacity)
	// evaluate again if the cache should evict items proactively
	hyperCache.shouldEvict.Swap(hyperCache.evictionInterval == 0 && hyperCache.backend.Capacity() > 0)

	// if the cache size is greater than the new capacity, evict items
	if hyperCache.backend.Count(ctx) > hyperCache.Capacity() {
		hyperCache.evictionLoop(ctx)
	}
}

// TriggerEviction sends a signal to the eviction loop to start.
func (hyperCache *HyperCache[T]) TriggerEviction(_ context.Context) {
	// Safe, non-blocking trigger; no-op if channel not initialized
	if hyperCache.evictCh == nil {
		return
	}

	select {
	case hyperCache.evictCh <- true:
	default:
	}
}
