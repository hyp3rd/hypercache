package hypercache

// Copyright 2023 F. All rights reserved.
// Use of this source code is governed by a Mozilla Public License 2.0
// license that can be found in the LICENSE file.
// HyperCache is a cache implementation for Go that supports multiple backends with the expiration and eviction of items.
// It can be used as a standalone cache or as a cache middleware for a service.
// It can implement a service interface to interact with the cache with middleware support (default or custom).

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hyp3rd/ewrap"

	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/internal/introspect"
	"github.com/hyp3rd/hypercache/internal/sentinel"
	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
	"github.com/hyp3rd/hypercache/pkg/eviction"
	"github.com/hyp3rd/hypercache/pkg/stats"
)

// HyperCache stores items with a key and optional expiration. It supports multiple backends
// and eviction algorithms. Configuration is provided via the Config struct using With* options.
// Background loops:
//   - expiration loop (interval: expirationInterval) scans for expired items
//   - eviction loop (interval: evictionInterval) evicts items via the configured algorithm
//
// Channels:
//   - expirationTriggerCh triggers an on-demand expiration pass (coalesced)
//   - evictCh triggers an immediate eviction pass when interval is 0 and capacity exceeded
//
// Synchronization:
//   - mutex protects eviction algorithm state
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
	expirationInterval         time.Duration       // interval for expiration loop
	evictionInterval           time.Duration       // interval for eviction loop
	shouldEvict                atomic.Bool         // proactive eviction enabled
	maxEvictionCount           uint                // max items per eviction run
	maxCacheSize               int64               // hard memory limit (MB), 0 = unlimited
	memoryAllocation           atomic.Int64        // current memory usage (bytes)
	mutex                      sync.RWMutex        // protects eviction algorithm
	once                       sync.Once           // ensures background loops start once
	statsCollectorName         string              // configured stats collector name
	// StatsCollector to collect cache statistics
	StatsCollector stats.ICollector
	// Optional management HTTP server
	mgmtHTTP *ManagementHTTPServer
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
	config := NewConfig[backend.InMemory](constants.InMemoryBackend)
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

// resolveBackend constructs a typed backend instance based on the config.BackendType.
func resolveBackend[T backend.IBackendConstrain](ctx context.Context, bm *BackendManager, config *Config[T]) (backend.IBackend[T], error) {
	constructor, exists := bm.backendRegistry[config.BackendType]
	if !exists {
		return nil, ewrap.Newf("unknown backend type: %s", config.BackendType)
	}

	switch config.BackendType {
	case constants.InMemoryBackend:
		return resolveInMemoryBackend[T](ctx, constructor, config)
	case constants.RedisBackend:
		return resolveRedisBackend[T](ctx, constructor, config)
	case constants.RedisClusterBackend:
		return resolveRedisClusterBackend[T](ctx, constructor, config)
	case constants.DistMemoryBackend:
		return resolveDistMemoryBackend[T](ctx, constructor, config)
	default:
		return nil, ewrap.Newf("unknown backend type: %s", config.BackendType)
	}
}

// castBackend tries to cast a backend instance of any concrete type to backend.IBackend[T].
func castBackend[T backend.IBackendConstrain](bi any) (backend.IBackend[T], error) {
	if b, ok := bi.(backend.IBackend[T]); ok {
		return b, nil
	}

	return nil, sentinel.ErrInvalidBackendType
}

func resolveInMemoryBackend[T backend.IBackendConstrain](ctx context.Context, constructor, cfgAny any) (backend.IBackend[T], error) {
	inMemCtor, ok := constructor.(InMemoryBackendConstructor)
	if !ok {
		return nil, sentinel.ErrInvalidBackendType
	}

	cfg, ok := cfgAny.(*Config[backend.InMemory])
	if !ok {
		return nil, sentinel.ErrInvalidBackendType
	}

	bi, err := inMemCtor.Create(ctx, cfg)
	if err != nil {
		return nil, err
	}

	return castBackend[T](bi)
}

func resolveRedisBackend[T backend.IBackendConstrain](ctx context.Context, constructor, cfgAny any) (backend.IBackend[T], error) {
	redisCtor, ok := constructor.(RedisBackendConstructor)
	if !ok {
		return nil, sentinel.ErrInvalidBackendType
	}

	cfg, ok := cfgAny.(*Config[backend.Redis])
	if !ok {
		return nil, sentinel.ErrInvalidBackendType
	}

	bi, err := redisCtor.Create(ctx, cfg)
	if err != nil {
		return nil, err
	}

	return castBackend[T](bi)
}

func resolveDistMemoryBackend[T backend.IBackendConstrain](ctx context.Context, constructor, cfgAny any) (backend.IBackend[T], error) {
	distCtor, ok := constructor.(DistMemoryBackendConstructor)
	if !ok {
		return nil, sentinel.ErrInvalidBackendType
	}

	cfg, ok := cfgAny.(*Config[backend.DistMemory])
	if !ok {
		return nil, sentinel.ErrInvalidBackendType
	}

	bi, err := distCtor.Create(ctx, cfg)
	if err != nil {
		return nil, err
	}

	return castBackend[T](bi)
}

func resolveRedisClusterBackend[T backend.IBackendConstrain](ctx context.Context, constructor, cfgAny any) (backend.IBackend[T], error) {
	clusterCtor, ok := constructor.(RedisClusterBackendConstructor)
	if !ok {
		return nil, sentinel.ErrInvalidBackendType
	}

	cfg, ok := cfgAny.(*Config[backend.RedisCluster])
	if !ok {
		return nil, sentinel.ErrInvalidBackendType
	}

	bi, err := clusterCtor.Create(ctx, cfg)
	if err != nil {
		return nil, err
	}

	return castBackend[T](bi)
}

// newHyperCacheBase builds the base HyperCache instance with default timings and internals.
func newHyperCacheBase[T backend.IBackendConstrain](b backend.IBackend[T]) *HyperCache[T] {
	return &HyperCache[T]{
		backend:            b,
		itemPoolManager:    cache.NewItemPoolManager(),
		workerPool:         NewWorkerPool(runtime.NumCPU()),
		stop:               make(chan bool, 2),
		evictCh:            make(chan bool, 1),
		expirationInterval: constants.DefaultExpirationInterval,
		evictionInterval:   constants.DefaultEvictionInterval,
	}
}

// configureEvictionSettings computes derived eviction settings like shouldEvict and default maxEvictionCount.
func configureEvictionSettings[T backend.IBackendConstrain](hc *HyperCache[T]) {
	hc.shouldEvict.Store(hc.evictionInterval == 0 && hc.backend.Capacity() > 0)

	if hc.maxEvictionCount == 0 {
		//nolint:gosec
		hc.maxEvictionCount = uint(hc.backend.Capacity())
	}
}

// initEvictionAlgorithm initializes the eviction algorithm for the cache.
func initEvictionAlgorithm[T backend.IBackendConstrain](hc *HyperCache[T]) error {
	var err error
	if hc.evictionAlgorithmName == "" {
		// Use the default eviction algorithm if none is specified
		//nolint:gosec
		hc.evictionAlgorithm, err = eviction.NewLRUAlgorithm(int(hc.maxEvictionCount))
	} else {
		// Use the specified eviction algorithm
		//nolint:gosec
		hc.evictionAlgorithm, err = eviction.NewEvictionAlgorithm(hc.evictionAlgorithmName, int(hc.maxEvictionCount))
	}

	return err
}

// configureStats sets the stats collector, using default if none specified.
func configureStats[T backend.IBackendConstrain](hc *HyperCache[T]) error {
	if hc.statsCollectorName == "" {
		hc.StatsCollector = stats.NewHistogramStatsCollector()

		return nil
	}

	var err error

	hc.StatsCollector, err = stats.NewCollector(hc.statsCollectorName)

	return err
}

// checkCapacity validates the backend capacity and returns an error if invalid.
func checkCapacity[T backend.IBackendConstrain](hc *HyperCache[T]) error {
	if hc.backend.Capacity() < 0 {
		return sentinel.ErrInvalidCapacity
	}

	return nil
}

// initExpirationTrigger initializes the expiration trigger channel with optional buffer override.
func initExpirationTrigger[T backend.IBackendConstrain](hc *HyperCache[T]) {
	buf := hc.backend.Capacity() / 2
	if hc.expirationTriggerBufSize > 0 {
		buf = hc.expirationTriggerBufSize
	}

	if buf < 1 {
		buf = 1
	}

	hc.expirationTriggerCh = make(chan bool, buf)
}

// startBackgroundJobs starts the background jobs for the hyper cache.
func (hyperCache *HyperCache[T]) startBackgroundJobs(ctx context.Context) {
	// Start expiration and eviction loops once
	hyperCache.once.Do(func() {
		// Long-lived background context, canceled on Stop
		jobsCtx, cancel := context.WithCancel(ctx)

		hyperCache.bgCancel = cancel

		hyperCache.startExpirationRoutine(jobsCtx)
		hyperCache.startEvictionRoutine(jobsCtx)
	})
}

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
	case <-hyperCache.stop:
		if tick != nil {
			tick.Stop()
		}

		return true
	}

	return false
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
			case <-hyperCache.stop:
				tick.Stop()

				return
			}
		}
	}()
}

// triggerExpiration coalesces and optionally debounces expiration triggers to avoid flooding the channel.
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

// expirationLoop is a function that runs in a separate goroutine and expires items in the cache based on their expiration duration.
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

// evictionLoop is a function that runs in a separate goroutine and evicts items from the cache based on the cache's capacity and the max
// eviction count.
// The eviction is determined by the eviction algorithm.
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

			// Protect eviction algorithm access
			hyperCache.mutex.Lock()

			key, ok := hyperCache.evictionAlgorithm.Evict()
			hyperCache.mutex.Unlock()

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

		hyperCache.StatsCollector.Gauge("item_count", int64(hyperCache.backend.Count(ctx)))
		//nolint:gosec
		hyperCache.StatsCollector.Gauge("evicted_item_count", int64(evictedCount))

		return nil
	})
}

// evictItem is a helper function that removes an item from the cache and returns the key of the evicted item.
// If no item can be evicted, it returns a false.
func (hyperCache *HyperCache[T]) evictItem(ctx context.Context) (string, bool) {
	hyperCache.mutex.Lock()

	key, ok := hyperCache.evictionAlgorithm.Evict()
	hyperCache.mutex.Unlock()

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

// Set adds an item to the cache with the given key and value. If an item with the same key already exists, it updates the value of the
// existing item.
// If the expiration duration is greater than zero, the item will expire after the specified duration.
// If capacity is reached:
//   - when evictionInterval == 0 we evict immediately
//   - otherwise the background eviction loop will reclaim space
func (hyperCache *HyperCache[T]) Set(ctx context.Context, key string, value any, expiration time.Duration) error {
	// Create a new cache item and set its properties
	item := hyperCache.itemPoolManager.Get()

	item.Key = key
	item.Value = value
	item.Expiration = expiration
	item.LastAccess = time.Now()

	// Set the size of the item (aligned with backend serializer when available)
	err := hyperCache.setItemSize(item)
	if err != nil {
		return err
	}

	// check if adding this item will exceed the maxCacheSize
	hyperCache.memoryAllocation.Add(item.Size)

	if hyperCache.maxCacheSize > 0 && hyperCache.memoryAllocation.Load() > hyperCache.maxCacheSize {
		hyperCache.memoryAllocation.Add(-item.Size)

		return sentinel.ErrCacheFull
	}

	// Insert the item into the cache
	err = hyperCache.backend.Set(ctx, item)
	if err != nil {
		hyperCache.memoryAllocation.Add(-item.Size)
		hyperCache.itemPoolManager.Put(item)

		return err
	}

	// Set the item in the eviction algorithm
	hyperCache.mutex.Lock()
	hyperCache.evictionAlgorithm.Set(key, item.Value)
	hyperCache.mutex.Unlock()

	// If the cache is at capacity, evict an item when the eviction interval is zero
	if hyperCache.shouldEvict.Load() && hyperCache.backend.Count(ctx) > hyperCache.backend.Capacity() {
		hyperCache.evictItem(ctx)
	}

	return nil
}

// Get retrieves the item with the given key from the cache returning the value and a boolean indicating if the item was found.
func (hyperCache *HyperCache[T]) Get(ctx context.Context, key string) (any, bool) {
	item, ok := hyperCache.backend.Get(ctx, key)
	if !ok {
		return nil, false
	}

	// Check if the item has expired, if so, trigger the expiration loop
	if item.Expired() {
		// Non-blocking trigger of expiration loop (do not return to pool yet; backend still holds it)
		// Coalesced/debounced trigger
		hyperCache.execTriggerExpiration()

		return nil, false
	}

	// Update the last access time and access count
	item.Touch()

	return item.Value, true
}

// GetWithInfo retrieves the item with the given key from the cache returning the `Item` object and a boolean indicating if the item was
// found.
func (hyperCache *HyperCache[T]) GetWithInfo(ctx context.Context, key string) (*cache.Item, bool) {
	item, ok := hyperCache.backend.Get(ctx, key)
	// Check if the item has expired if it exists, if so, trigger the expiration loop
	if !ok {
		return nil, false
	}

	// Check if the item has expired, if so, trigger the expiration loop
	if item.Expired() {
		// Non-blocking trigger of expiration loop; don't return to pool here
		// Coalesced/debounced trigger
		hyperCache.execTriggerExpiration()

		return nil, false
	}

	// Update the last access time and access count
	item.Touch()

	return item, true
}

// GetOrSet retrieves the item with the given key. If the item is not found, it adds the item to the cache with the given value and
// expiration duration.
// If the capacity of the cache is reached, leverage the eviction algorithm.
func (hyperCache *HyperCache[T]) GetOrSet(ctx context.Context, key string, value any, expiration time.Duration) (any, error) {
	// if the item is found, return the value
	if item, ok := hyperCache.backend.Get(ctx, key); ok {
		// Check if the item has expired
		if item.Expired() {
			// Non-blocking trigger of expiration loop; don't pool here to avoid zeroing live refs
			// Coalesced/debounced trigger
			hyperCache.execTriggerExpiration()

			return nil, sentinel.ErrKeyExpired
		}

		// Update the last access time and access count
		item.Touch()

		return item.Value, nil
	}

	// if the item is not found, add it to the cache
	item := hyperCache.itemPoolManager.Get()

	item.Key = key
	item.Value = value
	item.Expiration = expiration
	item.LastAccess = time.Now()

	// Set the size of the item (aligned with backend serializer when available)
	err := hyperCache.setItemSize(item)
	if err != nil {
		return nil, err
	}

	// check if adding this item will exceed the maxCacheSize
	hyperCache.memoryAllocation.Add(item.Size)

	if hyperCache.maxCacheSize > 0 && hyperCache.memoryAllocation.Load() > hyperCache.maxCacheSize {
		hyperCache.memoryAllocation.Add(-item.Size)
		hyperCache.itemPoolManager.Put(item)

		return nil, sentinel.ErrCacheFull
	}

	// Insert the item into the cache
	err = hyperCache.backend.Set(ctx, item)
	if err != nil {
		hyperCache.memoryAllocation.Add(-item.Size)
		hyperCache.itemPoolManager.Put(item)

		return nil, err
	}

	go func() {
		// Set the item in the eviction algorithm
		hyperCache.mutex.Lock()
		hyperCache.evictionAlgorithm.Set(key, item.Value)
		hyperCache.mutex.Unlock()
		// If the cache is at capacity, evict an item when the eviction interval is zero
		if hyperCache.shouldEvict.Load() && hyperCache.backend.Count(ctx) > hyperCache.backend.Capacity() {
			hyperCache.evictItem(ctx)
		}
	}()

	return value, nil
}

// GetMultiple retrieves the items with the given keys from the cache.
func (hyperCache *HyperCache[T]) GetMultiple(ctx context.Context, keys ...string) (map[string]any, map[string]error) {
	result := make(map[string]any, len(keys))   // Preallocate the result map
	failed := make(map[string]error, len(keys)) // Preallocate the errors map

	for _, key := range keys {
		item, ok := hyperCache.backend.Get(ctx, key)
		if !ok {
			// Add the key to the errors map and continue
			failed[key] = sentinel.ErrKeyNotFound

			continue
		}

		// Check if the item has expired
		if item.Expired() {
			// Treat expired items as not found per API semantics; don't pool here to avoid zeroing live refs
			failed[key] = sentinel.ErrKeyNotFound
			// Coalesced/debounced trigger of the expiration loop via channel
			hyperCache.execTriggerExpiration()
		} else {
			item.Touch() // Update the last access time and access count
			// Add the item to the result map
			result[key] = item.Value
		}
	}

	return result, failed
}

// List lists the items in the cache that meet the specified criteria.
// It takes in a variadic number of any type as filters, it then checks the backend type, and calls the corresponding
// implementation of the List function for that backend, with the filters passed in as arguments.
func (hyperCache *HyperCache[T]) List(ctx context.Context, filters ...backend.IFilter) ([]*cache.Item, error) {
	return hyperCache.backend.List(ctx, filters...)
}

// setItemSize computes item.Size using the backend serializer when available for accuracy.
// Falls back to the Item's internal SetSize when no serializer is present.
func (hyperCache *HyperCache[T]) setItemSize(item *cache.Item) error {
	// Prefer backend-specific serialization for accurate size accounting.
	switch backendImpl := any(hyperCache.backend).(type) {
	case *backend.Redis:
		if backendImpl.Serializer != nil {
			data, err := backendImpl.Serializer.Marshal(item.Value)
			if err != nil {
				return err
			}

			item.Size = int64(len(data))

			return nil
		}

	case *backend.RedisCluster:
		if backendImpl.Serializer != nil {
			data, err := backendImpl.Serializer.Marshal(item.Value)
			if err != nil {
				return err
			}

			item.Size = int64(len(data))

			return nil
		}
	}

	return item.SetSize()
}

// Remove removes items with the given key from the cache. If an item is not found, it does nothing.
func (hyperCache *HyperCache[T]) Remove(ctx context.Context, keys ...string) error {
	// Remove the item from the eviction algorithm
	// and update the memory allocation
	for _, key := range keys {
		item, ok := hyperCache.backend.Get(ctx, key)
		if ok {
			// remove the item from the cacheBackend and update the memory allocation
			hyperCache.memoryAllocation.Add(-item.Size)
			hyperCache.mutex.Lock()
			hyperCache.evictionAlgorithm.Delete(key)
			hyperCache.mutex.Unlock()
		}
	}

	err := hyperCache.backend.Remove(ctx, keys...)
	if err != nil {
		return err
	}

	return nil
}

// Clear removes all items from the cache.
func (hyperCache *HyperCache[T]) Clear(ctx context.Context) error {
	var (
		items []*cache.Item
		err   error
	)

	// get all expired items
	items, err = hyperCache.backend.List(ctx)
	if err != nil {
		return err
	}

	// clear the cacheBackend
	err = hyperCache.backend.Clear(ctx)
	if err != nil {
		return err
	}

	for _, item := range items {
		hyperCache.mutex.Lock()
		hyperCache.evictionAlgorithm.Delete(item.Key)
		hyperCache.mutex.Unlock()
	}

	// reset the memory allocation
	hyperCache.memoryAllocation.Store(0)

	return err
}

// Capacity returns the capacity of the cache.
func (hyperCache *HyperCache[T]) Capacity() int {
	return hyperCache.backend.Capacity()
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

// TriggerExpiration exposes a manual expiration trigger (debounced/coalesced internally).
func (hyperCache *HyperCache[T]) TriggerExpiration() { hyperCache.execTriggerExpiration() }

// EvictionInterval returns configured eviction interval.
func (hyperCache *HyperCache[T]) EvictionInterval() time.Duration { return hyperCache.evictionInterval }

// ExpirationInterval returns configured expiration interval.
func (hyperCache *HyperCache[T]) ExpirationInterval() time.Duration {
	return hyperCache.expirationInterval
}

// EvictionAlgorithm returns eviction algorithm name.
func (hyperCache *HyperCache[T]) EvictionAlgorithm() string { return hyperCache.evictionAlgorithmName }

const (
	shutdownTimeout = 2 * time.Second
)

// Stop function stops the expiration and eviction loops and closes the stop channel.
func (hyperCache *HyperCache[T]) Stop(ctx context.Context) error {
	// Stop the expiration and eviction loops
	wg := sync.WaitGroup{}

	wg.Go(func() {
		hyperCache.stop <- true
	})

	wg.Wait()

	if hyperCache.bgCancel != nil {
		hyperCache.bgCancel()
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

		cancel()
	}

	return nil
}

// GetStats returns the stats collected by the cache.
func (hyperCache *HyperCache[T]) GetStats() stats.Stats {
	// Lock the cache's mutex to ensure thread-safety
	hyperCache.mutex.RLock()
	defer hyperCache.mutex.RUnlock()

	// Get the stats from the stats collector
	statsOut := hyperCache.StatsCollector.GetStats()

	return statsOut
}

// DistMetrics returns distributed backend metrics if the underlying backend is DistMemory.
// Returns nil if unsupported.
func (hyperCache *HyperCache[T]) DistMetrics() any { // generic any to avoid exporting type into core interface
	if dm, ok := any(hyperCache.backend).(*backend.DistMemory); ok {
		m := dm.Metrics()

		return m
	}

	return nil
}

// ClusterOwners returns the owners for a key if the distributed backend supports it; otherwise empty slice.
func (hyperCache *HyperCache[T]) ClusterOwners(key string) []string {
	if dm, ok := any(hyperCache.backend).(*backend.DistMemory); ok {
		owners := dm.DebugOwners(key)

		out := make([]string, 0, len(owners))
		for _, o := range owners {
			out = append(out, string(o))
		}

		return out
	}

	return nil
}

// DistMembershipSnapshot returns a snapshot of membership if distributed backend; otherwise nil slice.
//
//nolint:nonamedreturns
func (hyperCache *HyperCache[T]) DistMembershipSnapshot() (members []struct {
	ID          string
	Address     string
	State       string
	Incarnation uint64
}, replication, vnodes int,
) { //nolint:ireturn
	if dm, ok := any(hyperCache.backend).(*backend.DistMemory); ok {
		membership := dm.Membership()
		ring := dm.Ring()

		if membership == nil || ring == nil {
			return nil, 0, 0
		}

		nodes := membership.List()
		out := make([]struct {
			ID          string
			Address     string
			State       string
			Incarnation uint64
		}, 0, len(nodes))

		for _, node := range nodes {
			out = append(out, struct {
				ID          string
				Address     string
				State       string
				Incarnation uint64
			}{
				ID:          string(node.ID),
				Address:     node.Address,
				State:       node.State.String(),
				Incarnation: node.Incarnation,
			})
		}

		return out, ring.Replication(), ring.VirtualNodesPerNode()
	}

	return nil, 0, 0
}

// DistRingHashSpots returns vnode hashes as hex strings if available (debug).
func (hyperCache *HyperCache[T]) DistRingHashSpots() []string { //nolint:ireturn
	if dm, ok := any(hyperCache.backend).(*backend.DistMemory); ok {
		if ring := dm.Ring(); ring != nil {
			return ring.VNodeHashes()
		}
	}

	return nil
}

// DistHeartbeatMetrics returns distributed heartbeat metrics if supported.
func (hyperCache *HyperCache[T]) DistHeartbeatMetrics() any { //nolint:ireturn
	if dm, ok := any(hyperCache.backend).(*backend.DistMemory); ok {
		m := dm.Metrics()

		return map[string]any{
			"heartbeatSuccess":   m.HeartbeatSuccess,
			"heartbeatFailure":   m.HeartbeatFailure,
			"nodesRemoved":       m.NodesRemoved,
			"readPrimaryPromote": m.ReadPrimaryPromote,
		}
	}

	return nil
}

// ManagementHTTPAddress returns the bound address of the optional management HTTP server.
// Empty string when the server is disabled or failed to start.
func (hyperCache *HyperCache[T]) ManagementHTTPAddress() string {
	if hyperCache.mgmtHTTP == nil {
		return ""
	}

	return hyperCache.mgmtHTTP.Address()
}
