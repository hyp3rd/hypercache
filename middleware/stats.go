package middleware

import (
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/cache"
	"github.com/hyp3rd/hypercache/stats"
)

// StatsCollectorMiddleware is a middleware that collects stats. It can and should re-use the same stats collector as the hypercache.
// Must implement the hypercache.Service interface.
type StatsCollectorMiddleware struct {
	next           hypercache.Service
	statsCollector stats.StatsCollector
}

// NewStatsCollectorMiddleware returns a new StatsCollectorMiddleware
func NewStatsCollectorMiddleware(next hypercache.Service, statsCollector stats.StatsCollector) hypercache.Service {
	return &StatsCollectorMiddleware{next: next, statsCollector: statsCollector}
}

// Get
func (mw StatsCollectorMiddleware) Get(key string) (interface{}, bool) {
	start := time.Now()
	defer func() {
		mw.statsCollector.Timing("hypercache_get_duration", time.Since(start).Nanoseconds())
		mw.statsCollector.Incr("hypercache_get_count", 1)
	}()
	return mw.next.Get(key)
}

// Set
func (mw StatsCollectorMiddleware) Set(key string, value any, expiration time.Duration) error {
	start := time.Now()
	defer func() {
		mw.statsCollector.Timing("hypercache_set_duration", time.Since(start).Nanoseconds())
		mw.statsCollector.Incr("hypercache_set_count", 1)
	}()
	return mw.next.Set(key, value, expiration)
}

// GetOrSet
func (mw StatsCollectorMiddleware) GetOrSet(key string, value any, expiration time.Duration) (any, error) {
	start := time.Now()
	defer func() {
		mw.statsCollector.Timing("hypercache_get_or_set_duration", time.Since(start).Nanoseconds())
		mw.statsCollector.Incr("hypercache_get_or_set_count", 1)
	}()
	return mw.next.GetOrSet(key, value, expiration)
}

// GetMultiple
func (mw StatsCollectorMiddleware) GetMultiple(keys ...string) (result map[string]any, failed map[string]error) {
	start := time.Now()
	defer func() {
		mw.statsCollector.Timing("hypercache_get_multiple_duration", time.Since(start).Nanoseconds())
		mw.statsCollector.Incr("hypercache_get_multiple_count", 1)
	}()
	return mw.next.GetMultiple(keys...)
}

// List
func (mw StatsCollectorMiddleware) List(filters ...any) ([]*cache.Item, error) {
	start := time.Now()
	defer func() {
		mw.statsCollector.Timing("hypercache_list_duration", time.Since(start).Nanoseconds())
		mw.statsCollector.Incr("hypercache_list_count", 1)
	}()
	return mw.next.List(filters...)
}

func (mw StatsCollectorMiddleware) Remove(keys ...string) {
	start := time.Now()
	defer func() {
		mw.statsCollector.Timing("hypercache_remove_duration", time.Since(start).Nanoseconds())
		mw.statsCollector.Incr("hypercache_remove_count", 1)
	}()
	mw.next.Remove(keys...)
}

// Clear
func (mw StatsCollectorMiddleware) Clear() error {
	start := time.Now()
	defer func() {
		mw.statsCollector.Timing("hypercache_clear_duration", time.Since(start).Nanoseconds())
		mw.statsCollector.Incr("hypercache_clear_count", 1)
	}()
	return mw.next.Clear()
}

// Capacity
func (mw StatsCollectorMiddleware) Capacity() int {
	return mw.next.Capacity()
}

// TriggerEviction
func (mw StatsCollectorMiddleware) TriggerEviction() {
	start := time.Now()
	defer func() {
		mw.statsCollector.Timing("hypercache_trigger_eviction_duration", time.Since(start).Nanoseconds())
		mw.statsCollector.Incr("hypercache_trigger_eviction_count", 1)
	}()
	mw.next.TriggerEviction()
}

// Size
func (mw StatsCollectorMiddleware) Size() int {
	return mw.next.Size()
}

// Stop stops the cache
func (mw StatsCollectorMiddleware) Stop() {
	start := time.Now()
	defer func() {
		mw.statsCollector.Timing("hypercache_stop_duration", time.Since(start).Nanoseconds())
		mw.statsCollector.Incr("hypercache_stop_count", 1)
	}()
	mw.next.Stop()
}

// GetStats
func (mw StatsCollectorMiddleware) GetStats() stats.Stats {
	return mw.next.GetStats()
}
