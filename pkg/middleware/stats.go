// Package middleware provides various middleware implementations for the hypercache service.
// This package includes stats middleware that collects and reports cache operation statistics.
package middleware

import (
	"context"
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/pkg/backend"
	"github.com/hyp3rd/hypercache/pkg/cache"
	"github.com/hyp3rd/hypercache/pkg/stats"
)

// StatsCollectorMiddleware is a middleware that collects stats. It can and should re-use the same stats collector as the hypercache.
// Must implement the hypercache.Service interface.
type StatsCollectorMiddleware struct {
	next           hypercache.Service
	statsCollector stats.ICollector
}

// NewStatsCollectorMiddleware returns a new StatsCollectorMiddleware.
func NewStatsCollectorMiddleware(next hypercache.Service, statsCollector stats.ICollector) hypercache.Service {
	return &StatsCollectorMiddleware{next: next, statsCollector: statsCollector}
}

// Get collects stats for the Get method.
func (mw StatsCollectorMiddleware) Get(ctx context.Context, key string) (any, bool) {
	start := time.Now()

	defer func() {
		mw.statsCollector.Timing("hypercache_get_duration", time.Since(start).Nanoseconds())
		mw.statsCollector.Incr("hypercache_get_count", 1)
	}()

	return mw.next.Get(ctx, key)
}

// Set collects stats for the Set method.
func (mw StatsCollectorMiddleware) Set(ctx context.Context, key string, value any, expiration time.Duration) error {
	start := time.Now()

	defer func() {
		mw.statsCollector.Timing("hypercache_set_duration", time.Since(start).Nanoseconds())
		mw.statsCollector.Incr("hypercache_set_count", 1)
	}()

	return mw.next.Set(ctx, key, value, expiration)
}

// GetOrSet collects stats for the GetOrSet method.
func (mw StatsCollectorMiddleware) GetOrSet(ctx context.Context, key string, value any, expiration time.Duration) (any, error) {
	start := time.Now()

	defer func() {
		mw.statsCollector.Timing("hypercache_get_or_set_duration", time.Since(start).Nanoseconds())
		mw.statsCollector.Incr("hypercache_get_or_set_count", 1)
	}()

	return mw.next.GetOrSet(ctx, key, value, expiration)
}

// GetWithInfo collects stats for the GetWithInfo method.
func (mw StatsCollectorMiddleware) GetWithInfo(ctx context.Context, key string) (*cache.Item, bool) {
	start := time.Now()

	defer func() {
		mw.statsCollector.Timing("hypercache_get_with_info_duration", time.Since(start).Nanoseconds())
		mw.statsCollector.Incr("hypercache_get_with_info_count", 1)
	}()

	return mw.next.GetWithInfo(ctx, key)
}

// GetMultiple collects stats for the GetMultiple method.
func (mw StatsCollectorMiddleware) GetMultiple(ctx context.Context, keys ...string) (map[string]any, map[string]error) {
	start := time.Now()

	defer func() {
		mw.statsCollector.Timing("hypercache_get_multiple_duration", time.Since(start).Nanoseconds())
		mw.statsCollector.Incr("hypercache_get_multiple_count", 1)
	}()

	return mw.next.GetMultiple(ctx, keys...)
}

// List collects stats for the List method.
func (mw StatsCollectorMiddleware) List(ctx context.Context, filters ...backend.IFilter) ([]*cache.Item, error) {
	start := time.Now()

	defer func() {
		mw.statsCollector.Timing("hypercache_list_duration", time.Since(start).Nanoseconds())
		mw.statsCollector.Incr("hypercache_list_count", 1)
	}()

	return mw.next.List(ctx, filters...)
}

// Remove collects stats for the Remove method.
func (mw StatsCollectorMiddleware) Remove(ctx context.Context, keys ...string) error {
	start := time.Now()

	defer func() {
		mw.statsCollector.Timing("hypercache_remove_duration", time.Since(start).Nanoseconds())
		mw.statsCollector.Incr("hypercache_remove_count", 1)
	}()

	return mw.next.Remove(ctx, keys...)
}

// Clear collects stats for the Clear method.
func (mw StatsCollectorMiddleware) Clear(ctx context.Context) error {
	start := time.Now()

	defer func() {
		mw.statsCollector.Timing("hypercache_clear_duration", time.Since(start).Nanoseconds())
		mw.statsCollector.Incr("hypercache_clear_count", 1)
	}()

	return mw.next.Clear(ctx)
}

// Capacity returns the capacity of the cache.
func (mw StatsCollectorMiddleware) Capacity() int {
	return mw.next.Capacity()
}

// TriggerEviction triggers the eviction of the cache.
func (mw StatsCollectorMiddleware) TriggerEviction() {
	start := time.Now()

	defer func() {
		mw.statsCollector.Timing("hypercache_trigger_eviction_duration", time.Since(start).Nanoseconds())
		mw.statsCollector.Incr("hypercache_trigger_eviction_count", 1)
	}()

	mw.next.TriggerEviction()
}

// Allocation returns the size allocation in bytes cache.
func (mw StatsCollectorMiddleware) Allocation() int64 {
	return mw.next.Allocation()
}

// Count returns the count of the items in the cache.
func (mw StatsCollectorMiddleware) Count(ctx context.Context) int {
	return mw.next.Count(ctx)
}

// Stop collects the stats for Stop methods and stops the cache and all its goroutines (if any).
func (mw StatsCollectorMiddleware) Stop(ctx context.Context) error {
	start := time.Now()

	defer func() {
		mw.statsCollector.Timing("hypercache_stop_duration", time.Since(start).Nanoseconds())
		mw.statsCollector.Incr("hypercache_stop_count", 1)
	}()

	return mw.next.Stop(ctx)
}

// GetStats returns the stats of the cache.
func (mw StatsCollectorMiddleware) GetStats() stats.Stats {
	return mw.next.GetStats()
}
