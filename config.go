// Package hypercache provides a high-performance, generic caching library with configurable backends and eviction algorithms.
// It supports multiple backend types including in-memory and Redis, with various eviction strategies like LRU, LFU, and more.
// The package is designed to be flexible and extensible, allowing users to customize cache behavior through configuration options.
//
// Example usage:
//
//	config, err := hypercache.NewConfig[string]("inmemory")
//	if err != nil { /* ... */ }
//	cache := hypercache.NewHyperCache[string](config)
//	cache.Set("key", "value", time.Hour)
//	value, found := cache.Get("key")
package hypercache

import (
	"log/slog"
	"strings"
	"time"

	"github.com/hyp3rd/ewrap"

	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/internal/sentinel"
	"github.com/hyp3rd/hypercache/pkg/backend"
)

// Config is a struct that wraps all the configuration options to setup `HyperCache` and its backend.
type Config[T backend.IBackendConstrain] struct {
	// BackendType is the type of the backend to use.
	BackendType string
	// InMemoryOptions is a slice of options that can be used to configure the `InMemory`.
	InMemoryOptions []backend.Option[backend.InMemory]
	// RedisOptions is a slice of options that can be used to configure the `Redis`.
	RedisOptions []backend.Option[backend.Redis]
	// RedisClusterOptions is a slice of options to configure the `RedisCluster` backend.
	RedisClusterOptions []backend.Option[backend.RedisCluster]
	// DistMemoryOptions configure the distributed in-memory (sharded) backend.
	DistMemoryOptions []backend.DistMemoryOption
	// HyperCacheOptions is a slice of options that can be used to configure `HyperCache`.
	HyperCacheOptions []Option[T]
}

// NewConfig returns a new `Config` struct with default values:
//   - `InMemoryOptions` is empty
//   - `RedisOptions` is empty
//   - `HyperCacheOptions` is set to:
//     -- `WithExpirationInterval[T](30 * time.Minute)`
//     -- `WithEvictionAlgorithm[T]("lfu")`
//     -- `WithEvictionInterval[T](10 * time.Minute)`
//
// Each of the above options can be overridden by passing a different option to the `NewConfig` function.
// It can be used to configure `HyperCache` and its backend and customize the behavior of the cache.
//
// NewConfig returns sentinel.ErrParamCannotBeEmpty if backendType is empty or
// whitespace-only. Previous versions panicked in that case; v2 returns the
// error so callers can handle invalid configuration without unwinding.
func NewConfig[T backend.IBackendConstrain](backendType string) (*Config[T], error) {
	if strings.TrimSpace(backendType) == "" {
		return nil, ewrap.Wrap(sentinel.ErrParamCannotBeEmpty, "backendType")
	}

	return &Config[T]{
		BackendType:         backendType,
		InMemoryOptions:     []backend.Option[backend.InMemory]{},
		RedisOptions:        []backend.Option[backend.Redis]{},
		RedisClusterOptions: []backend.Option[backend.RedisCluster]{},
		DistMemoryOptions:   []backend.DistMemoryOption{},
		HyperCacheOptions: []Option[T]{
			WithExpirationInterval[T](constants.DefaultExpirationInterval),
			WithEvictionAlgorithm[T]("lfu"),
			WithEvictionInterval[T](constants.DefaultEvictionInterval),
		},
	}, nil
}

// Option is a function type that can be used to configure the `HyperCache` struct.
type Option[T backend.IBackendConstrain] func(*HyperCache[T])

// ApplyHyperCacheOptions applies the given options to the given cache.
func ApplyHyperCacheOptions[T backend.IBackendConstrain](cache *HyperCache[T], options ...Option[T]) {
	for _, option := range options {
		option(cache)
	}
}

// WithManagementHTTP enables the optional management HTTP server with the provided address and options.
// addr format example: ":8080" or "127.0.0.1:9090".
func WithManagementHTTP[T backend.IBackendConstrain](addr string, opts ...ManagementHTTPOption) Option[T] {
	return func(cache *HyperCache[T]) {
		if addr == "" { // noop when empty
			return
		}

		cache.mgmtHTTP = NewManagementHTTPServer(addr, opts...)
	}
}

// WithMaxCacheSize is an option that sets the maximum size of the cache.
// The maximum size of the cache is the maximum number of items that can be stored in the cache.
// If the maximum size of the cache is reached, the least recently used item will be evicted from the cache.
func WithMaxCacheSize[T backend.IBackendConstrain](maxCacheSize int64) Option[T] {
	return func(cache *HyperCache[T]) {
		// If the max cache size is less than 0, set it to 0.
		if maxCacheSize < 0 {
			maxCacheSize = 0
		}

		cache.maxCacheSize = maxCacheSize
	}
}

// WithEvictionShardCount sets the number of independent eviction-algorithm
// shards. Default 32 (matches pkg/cache/v2.ShardCount). Must be a positive
// power of two; values <= 1 disable sharding (single global eviction
// instance — strict global LRU/LFU order, but single-mutex contention).
//
// With sharding enabled, total capacity is split as ceil(capacity/n) per
// shard. Eviction order is per-shard, not strict global. See
// pkg/eviction.Sharded for full semantics.
func WithEvictionShardCount[T backend.IBackendConstrain](shardCount int) Option[T] {
	return func(cache *HyperCache[T]) {
		cache.evictionShardCount = shardCount
	}
}

// WithEvictionAlgorithm is an option that sets the eviction algorithm name field of the `HyperCache` struct.
// The eviction algorithm name determines which eviction algorithm will be used to evict items from the cache.
// The eviction algorithm name must be one of the following:
//   - "LRU" (Least Recently Used) - Implemented in the `eviction/lru.go` file
//   - "LFU" (Least Frequently Used) - Implemented in the `eviction/lfu.go` file
//   - "CAWOLFU" (Cache-Aware Write-Optimized LFU) - Implemented in the `eviction/cawolfu.go` file
//   - "FIFO" (First In First Out)
//   - "RANDOM" (Random)
//   - "CLOCK" (Clock) - Implemented in the `eviction/clock.go` file
//   - "ARC" (Adaptive Replacement Cache) - Experimental (not enabled by default)
//   - "TTL" (Time To Live)
//   - "LFUDA" (Least Frequently Used with Dynamic Aging)
//   - "SLRU" (Segmented Least Recently Used)
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

// WithLogger sets the structured logger used by HyperCache for background-
// loop and lifecycle events: eviction-loop trigger, expiration-loop trigger,
// management HTTP server startup, and (when paired with a DistMemory
// backend) the cluster-shape summary at boot.
//
// Defaults to a discard handler so library-mode embedded uses stay silent.
// The hypercache-server binary wires its own slog.Logger here so operators
// see structured JSON for every background task without needing the OTel
// metrics pipeline. Pass nil to reset to the default discard handler.
//
// Note: the DistMemory backend has its own WithDistLogger option for
// distributed-transport events (heartbeat, hint replay, rebalance). The
// two are intentionally separate so an embedded user can silence one
// surface without affecting the other.
func WithLogger[T backend.IBackendConstrain](logger *slog.Logger) Option[T] {
	return func(cache *HyperCache[T]) {
		if logger == nil {
			cache.logger = slog.New(slog.DiscardHandler)

			return
		}

		cache.logger = logger
	}
}

// WithExpirationTriggerBuffer sets the buffer size of the expiration trigger channel.
// If set to <= 0, the default (capacity/2, minimum 1) is used.
func WithExpirationTriggerBuffer[T backend.IBackendConstrain](size int) Option[T] {
	return func(cache *HyperCache[T]) {
		cache.expirationTriggerBufSize = size
	}
}

// WithExpirationTriggerDebounce sets an optional debounce interval for coalescing expiration triggers.
// Triggers arriving within this interval after the last accepted trigger may be dropped.
func WithExpirationTriggerDebounce[T backend.IBackendConstrain](interval time.Duration) Option[T] {
	return func(cache *HyperCache[T]) {
		cache.expirationDebounceInterval = interval
	}
}

// WithMaxEvictionCount is an option that sets the max eviction count field of the `HyperCache` struct.
// The max eviction count determines the maximum number of items that can be removed during a single eviction run.
func WithMaxEvictionCount[T backend.IBackendConstrain](maxEvictionCount uint) Option[T] {
	return func(cache *HyperCache[T]) {
		cache.maxEvictionCount = maxEvictionCount
	}
}
