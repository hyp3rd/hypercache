package hypercache

import (
	"time"

	"github.com/hyp3rd/hypercache/types"
)

// Option is a function type that can be used to configure the `HyperCache` struct.
type Option func(*HyperCache)

// WithStatsCollector is an option that sets the stats collector field of the `HyperCache` struct.
// The stats collector is used to collect statistics about the cache.
func WithStatsCollector(statsCollector StatsCollector) Option {
	return func(cache *HyperCache) {
		cache.statsCollector = statsCollector
	}
}

// WithExpirationInterval is an option that sets the expiration interval field of the `HyperCache` struct.
// The expiration interval determines how often the cache will check for and remove expired items.
func WithExpirationInterval(expirationInterval time.Duration) Option {
	return func(cache *HyperCache) {
		cache.expirationInterval = expirationInterval
	}
}

// WithEvictionTriggerBufferSize is an option that sets the eviction trigger buffer size field of the `HyperCache` struct.
// The eviction trigger buffer size determines how many items need to be added to the cache before an eviction run is triggered.
func WithEvictionTriggerBufferSize(evictionTriggerBufferSize uint) Option {
	return func(cache *HyperCache) {
		cache.evictionTriggerBufferSize = evictionTriggerBufferSize
	}
}

// WithEvictionInterval is an option that sets the eviction interval field of the `HyperCache` struct.
// The eviction interval determines how often the cache will run the eviction process to remove the least recently used items.
func WithEvictionInterval(evictionInterval time.Duration) Option {
	return func(cache *HyperCache) {
		cache.evictionInterval = evictionInterval
	}
}

// WithMaxEvictionCount is an option that sets the max eviction count field of the `HyperCache` struct.
// The max eviction count determines the maximum number of items that can be removed during a single eviction run.
func WithMaxEvictionCount(maxEvictionCount uint) Option {
	return func(cache *HyperCache) {
		cache.maxEvictionCount = maxEvictionCount
	}
}

// WithSortBy is an option that sets the field to sort the items by.
// The field can be any of the fields in the `CacheItem` struct.
func WithSortBy(field types.SortingField) Option {
	return func(cache *HyperCache) {
		cache.sortBy = field.String()
	}
}

// WithSortAscending is an option that sets the sort order to ascending.
// When sorting the items in the cache, they will be sorted in ascending order based on the field specified with the `WithSortBy` option.
func WithSortAscending() Option {
	return func(cache *HyperCache) {
		cache.sortAscending = true
	}
}

// WithSortDescending is an option that sets the sort order to descending.
// When sorting the items in the cache, they will be sorted in descending order based on the field specified with the `WithSortBy` option.
func WithSortDescending() Option {
	return func(cache *HyperCache) {
		cache.sortAscending = false
	}
}

// WithFilter is an option that sets the filter function to use.
// The filter function is a predicate that takes a `CacheItem` as an argument and returns a boolean indicating whether the item should be included in the cache.
func WithFilter(fn func(item *CacheItem) bool) Option {
	return func(cache *HyperCache) {
		cache.filterFn = fn
	}
}
