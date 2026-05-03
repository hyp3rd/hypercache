package hypercache

// Copyright 2023 F. All rights reserved.
// Use of this source code is governed by a Mozilla Public License 2.0
// license that can be found in the LICENSE file.
// HyperCache is a cache implementation for Go that supports multiple backends with the expiration and eviction of items.
// It can be used as a standalone cache or as a cache middleware for a service.
// It can implement a service interface to interact with the cache with middleware support (default or custom).

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/internal/introspect"
	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
	"github.com/hyp3rd/hypercache/pkg/eviction"
	"github.com/hyp3rd/hypercache/pkg/stats"
)

// HyperCache stores items with a key and optional expiration. It supports multiple backends
// and eviction algorithms. Configuration is provided via the Config struct using With* options.
//
// File layout (this package):
//   - hypercache.go            type, constructors (New, NewInMemoryWithDefaults), Stop, accessors
//   - hypercache_construct.go  backend resolution, base init, stats config, capacity check
//   - hypercache_io.go         Set/Get/GetWithInfo/GetOrSet/GetMultiple/Remove/Clear/List/touchItem
//   - hypercache_eviction.go   eviction loop + algorithm init + SetCapacity + TriggerEviction
//   - hypercache_expiration.go expiration loop + trigger debounce + TriggerExpiration
//   - hypercache_dist.go       DistMemory-only inspection methods
//
// Background loops:
//   - expiration loop (interval: expirationInterval) scans for expired items
//   - eviction loop (interval: evictionInterval) evicts items via the configured algorithm
//
// Channels:
//   - expirationTriggerCh triggers an on-demand expiration pass (coalesced)
//   - evictCh triggers an immediate eviction pass when interval is 0 and capacity exceeded
//
// Synchronization:
//   - Each eviction algorithm protects its own state internally
//   - stop channel signals background loops to stop
type HyperCache[T backend.IBackendConstrain] struct {
	backend             backend.IBackend[T]               // backend used to store items
	cacheBackendChecker introspect.CacheBackendChecker[T] // backend type helper
	itemPoolManager     *cache.ItemPoolManager            // manages pooled cache items
	stop                chan bool                         // stop signal for background loops
	// background cancel function for loops (context is created on start, canceled on Stop)
	bgCancel                   context.CancelFunc
	workerPool                 *WorkerPool         // worker pool for background tasks
	expirationTriggerCh        chan bool           // manual expiration triggers (coalesced)
	expirationTriggerBufSize   int                 // optional override for trigger channel size
	expirationSignalPending    atomic.Bool         // whether a trigger is pending
	expirationDebounceInterval time.Duration       // debounce between accepted triggers
	lastExpirationTrigger      atomic.Int64        // unix nano timestamp of last trigger
	evictCh                    chan bool           // manual eviction trigger
	evictionAlgorithmName      string              // name of eviction algorithm
	evictionAlgorithm          eviction.IAlgorithm // eviction algorithm impl
	evictionShardCount         int                 // number of eviction-algo shards (default 32; <=1 disables sharding)
	expirationInterval         time.Duration       // interval for expiration loop
	evictionInterval           time.Duration       // interval for eviction loop
	shouldEvict                atomic.Bool         // proactive eviction enabled
	maxEvictionCount           uint                // max items per eviction run
	maxCacheSize               int64               // hard memory limit (MB), 0 = unlimited
	memoryAllocation           atomic.Int64        // current memory usage (bytes)
	once                       sync.Once           // ensures background loops start once
	statsCollectorName         string              // configured stats collector name
	// StatsCollector to collect cache statistics
	StatsCollector stats.ICollector
	// Optional management HTTP server
	mgmtHTTP *ManagementHTTPServer
}

// touchBackend is the optional interface a backend implements when it wants
// to be notified that a key was just accessed — used by Get* methods to
// update LRU/clock state without imposing a hard dependency.
type touchBackend interface {
	Touch(ctx context.Context, key string) bool
}

// NewInMemoryWithDefaults initializes a new HyperCache with the default configuration.
// The default configuration is:
//   - The eviction interval is set to 10 minutes.
//   - The eviction algorithm is set to LRU.
//   - The expiration interval is set to 30 minutes.
//   - The capacity of the in-memory backend is set to 0 items (no limitations) unless specified.
//   - The maximum cache size in bytes is set to 0 (no limitations).
func NewInMemoryWithDefaults(ctx context.Context, capacity int) (*HyperCache[backend.InMemory], error) {
	// Initialize the configuration
	config, err := NewConfig[backend.InMemory](constants.InMemoryBackend)
	if err != nil {
		return nil, err
	}

	// Set the default options
	config.HyperCacheOptions = []Option[backend.InMemory]{
		WithEvictionInterval[backend.InMemory](constants.DefaultEvictionInterval),
		WithEvictionAlgorithm[backend.InMemory](constants.DefaultEvictionAlgorithm),
		WithExpirationInterval[backend.InMemory](constants.DefaultExpirationInterval),
	}

	// Set the in-memory backend options
	config.InMemoryOptions = []backend.Option[backend.InMemory]{
		backend.WithCapacity[backend.InMemory](capacity),
	}

	hcm := GetDefaultManager()

	// Initialize the cache
	hyperCache, err := New(ctx, hcm, config)
	if err != nil {
		return nil, err
	}

	return hyperCache, nil
}

// New initializes a new HyperCache with the given configuration.
// The default configuration is:
//   - The eviction interval is set to 5 minutes.
//   - The eviction algorithm is set to LRU.
//   - The expiration interval is set to 30 minutes.
//   - The stats collector is set to the HistogramStatsCollector stats collector.
func New[T backend.IBackendConstrain](ctx context.Context, bm *BackendManager, config *Config[T]) (*HyperCache[T], error) {
	// Resolve typed backend from registry
	backendTyped, err := resolveBackend(ctx, bm, config)
	if err != nil {
		return nil, err
	}

	// Initialize base cache struct
	hyperCache := newHyperCacheBase[T](backendTyped)

	// Initialize the cache backend type checker
	hyperCache.cacheBackendChecker = introspect.CacheBackendChecker[T]{
		Backend:     hyperCache.backend,
		BackendType: config.BackendType,
	}

	// Apply options and configure eviction-related settings
	ApplyHyperCacheOptions(hyperCache, config.HyperCacheOptions...)
	configureEvictionSettings(hyperCache)

	// Initialize eviction algorithm
	err = initEvictionAlgorithm(hyperCache)
	if err != nil {
		return hyperCache, err
	}

	// Stats collector
	err = configureStats(hyperCache)
	if err != nil {
		return hyperCache, err
	}

	// Capacity check (fatal)
	err = checkCapacity(hyperCache)
	if err != nil {
		return nil, err
	}

	// Initialize expiration trigger channel and start background jobs
	initExpirationTrigger(hyperCache)
	hyperCache.startBackgroundJobs(ctx)

	// Start optional management HTTP server (non-fatal if start fails)
	if hyperCache.mgmtHTTP != nil {
		err = hyperCache.mgmtHTTP.Start(ctx, hyperCache) // optional
		if err != nil {
			hyperCache.mgmtHTTP = nil
		}
	}

	return hyperCache, nil
}

// Stop signals background loops to stop, shuts down the worker pool, and
// closes any optional management HTTP server. The shutdown is bounded by
// shutdownTimeout to keep tests responsive.
func (hyperCache *HyperCache[T]) Stop(ctx context.Context) error {
	// Best-effort stop signal for listeners that still rely on stop channel.
	select {
	case hyperCache.stop <- true:
	default:
	}

	if hyperCache.bgCancel != nil {
		hyperCache.bgCancel()

		hyperCache.bgCancel = nil
	}

	hyperCache.once = sync.Once{}
	hyperCache.workerPool.Shutdown()

	if hyperCache.mgmtHTTP != nil {
		ctx, cancel := context.WithTimeout(ctx, shutdownTimeout)
		defer cancel()

		err := hyperCache.mgmtHTTP.Shutdown(ctx)
		if err != nil {
			// Handle error
			return err
		}
	}

	return nil
}

// Capacity returns the capacity of the cache.
func (hyperCache *HyperCache[T]) Capacity() int {
	return hyperCache.backend.Capacity()
}

// Allocation returns the size allocation in bytes of the current cache.
func (hyperCache *HyperCache[T]) Allocation() int64 {
	return hyperCache.memoryAllocation.Load()
}

// MaxCacheSize returns the maximum size in bytes of the cache.
func (hyperCache *HyperCache[T]) MaxCacheSize() int64 {
	return hyperCache.maxCacheSize
}

// Count returns the number of items in the cache.
func (hyperCache *HyperCache[T]) Count(ctx context.Context) int {
	return hyperCache.backend.Count(ctx)
}

// EvictionInterval returns configured eviction interval.
func (hyperCache *HyperCache[T]) EvictionInterval() time.Duration { return hyperCache.evictionInterval }

// ExpirationInterval returns configured expiration interval.
func (hyperCache *HyperCache[T]) ExpirationInterval() time.Duration {
	return hyperCache.expirationInterval
}

// EvictionAlgorithm returns eviction algorithm name.
func (hyperCache *HyperCache[T]) EvictionAlgorithm() string { return hyperCache.evictionAlgorithmName }

// GetStats returns the stats collected by the cache. Thread-safety is
// delegated to the configured StatsCollector implementation (the default
// HistogramStatsCollector is fully thread-safe with no global lock).
func (hyperCache *HyperCache[T]) GetStats() stats.Stats {
	return hyperCache.StatsCollector.GetStats()
}

// ManagementHTTPAddress returns the bound address of the optional management HTTP server.
// Empty string when the server is disabled or failed to start.
func (hyperCache *HyperCache[T]) ManagementHTTPAddress() string {
	if hyperCache.mgmtHTTP == nil {
		return ""
	}

	return hyperCache.mgmtHTTP.Address()
}
