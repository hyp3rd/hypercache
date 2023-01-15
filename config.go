package hypercache

import (
	"time"

	"github.com/hyp3rd/hypercache/backend"
)

// Config is a struct that wraps all the configuration options to setup `HyperCache` and its backend.
type Config[T backend.IBackendConstrain] struct {
	// InMemoryBackendOptions is a slice of options that can be used to configure the `InMemoryBackend`.
	InMemoryBackendOptions []backend.BackendOption[backend.InMemoryBackend]
	// RedisBackendOptions is a slice of options that can be used to configure the `RedisBackend`.
	RedisBackendOptions []backend.BackendOption[backend.RedisBackend]
	// HyperCacheOptions is a slice of options that can be used to configure `HyperCache`.
	HyperCacheOptions []Option[T]
}

// NewConfig returns a new `Config` struct with default values:
// - `InMemoryBackendOptions` is empty
// - `RedisBackendOptions` is empty
// - `HyperCacheOptions` is set to:
//   - `WithExpirationInterval[T](30 * time.Minute)`
//   - `WithEvictionAlgorithm[T]("lfu")`
//   - `WithEvictionInterval[T](10 * time.Minute)`
//
// Each of the above options can be overridden by passing a different option to the `NewConfig` function.
// It can be used to configure `HyperCache` and its backend and customize the behavior of the cache.
func NewConfig[T backend.IBackendConstrain]() *Config[T] {
	return &Config[T]{
		InMemoryBackendOptions: []backend.BackendOption[backend.InMemoryBackend]{},
		RedisBackendOptions:    []backend.BackendOption[backend.RedisBackend]{},
		HyperCacheOptions: []Option[T]{
			WithExpirationInterval[T](30 * time.Minute),
			WithEvictionAlgorithm[T]("lfu"),
			WithEvictionInterval[T](10 * time.Minute),
		},
	}
}

// Option is a function type that can be used to configure the `HyperCache` struct.
type Option[T backend.IBackendConstrain] func(*HyperCache[T])

// ApplyHyperCacheOptions applies the given options to the given cache.
func ApplyHyperCacheOptions[T backend.IBackendConstrain](cache *HyperCache[T], options ...Option[T]) {
	for _, option := range options {
		option(cache)
	}
}

// WithEvictionAlgorithm is an option that sets the eviction algorithm name field of the `HyperCache` struct.
// The eviction algorithm name determines which eviction algorithm will be used to evict items from the cache.
// The eviction algorithm name must be one of the following:
// - "LRU" (Least Recently Used) - Implemented in the `lru.go` file
// - "LFU" (Least Frequently Used) - Implemented in the `lfu.go` file
// - "CAWOLFU" (Cache-Aware Write-Optimized LFU) - Implemented in the `cawolfu.go` file
// - "FIFO" (First In First Out)
// - "RANDOM" (Random)
// - "CLOCK" (Clock) - Implemented in the `clock.go` file
// - "ARC" (Adaptive Replacement Cache) - Implemented in the `arc.go` file
// - "TTL" (Time To Live)
// - "LFUDA" (Least Frequently Used with Dynamic Aging)
// - "SLRU" (Segmented Least Recently Used)
func WithEvictionAlgorithm[T backend.IBackendConstrain](name string) Option[T] {
	return func(cache *HyperCache[T]) {
		cache.evictionAlgorithmName = name
	}
}

// WithStatsCollector is an option that sets the stats collector field of the `HyperCache` struct.
// The stats collector is used to collect statistics about the cache.
func WithStatsCollector[T backend.IBackendConstrain](name string) Option[T] {
	return func(cache *HyperCache[T]) {
		cache.statsCollectorName = name
	}
}

// WithExpirationInterval is an option that sets the expiration interval field of the `HyperCache` struct.
// The expiration interval determines how often the cache will check for and remove expired items.
func WithExpirationInterval[T backend.IBackendConstrain](expirationInterval time.Duration) Option[T] {
	return func(cache *HyperCache[T]) {
		cache.expirationInterval = expirationInterval
	}
}

// WithEvictionInterval is an option that sets the eviction interval field of the `HyperCache` struct.
// The eviction interval determines how often the cache will run the eviction process to remove the least recently used items.
func WithEvictionInterval[T backend.IBackendConstrain](evictionInterval time.Duration) Option[T] {
	return func(cache *HyperCache[T]) {
		cache.evictionInterval = evictionInterval
	}
}

// WithMaxEvictionCount is an option that sets the max eviction count field of the `HyperCache` struct.
// The max eviction count determines the maximum number of items that can be removed during a single eviction run.
func WithMaxEvictionCount[T backend.IBackendConstrain](maxEvictionCount uint) Option[T] {
	return func(cache *HyperCache[T]) {
		cache.maxEvictionCount = maxEvictionCount
	}
}
