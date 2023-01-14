package middleware

import (
	"fmt"
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/stats"
)

// StatsCollectorMiddleware is a middleware that collects stats. It can and should re-use the same stats collector as the hypercache.
// Must implement the hypercache.HyperCacheService interface.
type StatsCollectorMiddleware struct {
	next           hypercache.HyperCacheService
	statsCollector stats.StatsCollector
}

// NewStatsCollectorMiddleware returns a new StatsCollectorMiddleware
func (mw StatsCollectorMiddleware) Get(key string) (interface{}, bool) {
	start := time.Now()
	defer func() {
		fmt.Println("method Get from stats")

		mw.statsCollector.Timing("hypercache_get_duration", time.Since(start).Nanoseconds())
		mw.statsCollector.Incr("hypercache_get_count", 1)
	}()
	return mw.next.Get(key)
}

// NewStatsCollectorMiddleware returns a new StatsCollectorMiddleware
func (mw StatsCollectorMiddleware) Set(key string, value any, expiration time.Duration) error {
	start := time.Now()
	defer func() {
		fmt.Println("method Set from stats")

		mw.statsCollector.Timing("hypercache_set_duration", time.Since(start).Nanoseconds())
		mw.statsCollector.Incr("hypercache_set_count", 1)
	}()
	return mw.next.Set(key, value, expiration)
}

// NewStatsCollectorMiddleware returns a new StatsCollectorMiddleware
func (mw StatsCollectorMiddleware) GetOrSet(key string, value any, expiration time.Duration) (any, error) {
	start := time.Now()
	defer func() {
		mw.statsCollector.Timing("hypercache_set_duration", time.Since(start).Nanoseconds())
		mw.statsCollector.Incr("hypercache_set_count", 1)
	}()

	return mw.next.GetOrSet(key, value, expiration)
}

// NewStatsCollectorMiddleware returns a new StatsCollectorMiddleware
func NewStatsCollectorMiddleware(next hypercache.HyperCacheService, statsCollector stats.StatsCollector) hypercache.HyperCacheService {
	return &StatsCollectorMiddleware{next: next, statsCollector: statsCollector}
}
