package hypercache

// Copyright 2023 F. All rights reserved.
// Use of this source code is governed by a Mozilla Public License 2.0
// license that can be found in the LICENSE file.
// HyperCache is a cache implementation for Go that supports multiple backends with the expiration and eviction of items.
// It can be used as a standalone cache or as a cache middleware for a service.
// It can implement a service interface to interact with the cache with middleware support (default or custom).

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hyp3rd/hypercache/backend"
	"github.com/hyp3rd/hypercache/errors"
	"github.com/hyp3rd/hypercache/eviction"
	"github.com/hyp3rd/hypercache/stats"
	"github.com/hyp3rd/hypercache/types"
	"github.com/hyp3rd/hypercache/utils"
)

// HyperCache is a cache that stores items with a key and expiration duration. It supports multiple backends and multiple eviction algorithms.
// The default in-memory implementation has a custom `ConcurrentMap` to store the items in the cache,
// The configuration is provided by the `Config` struct and can be customized by using the `With` functions.
// The cache has two loops that run in the background:
//   - The expiration loop runs every `expirationInterval` and checks for expired items.
//   - The eviction loop runs every `evictionInterval` and evicts items using the eviction algorithm.
//
// The cache leverages two channels to signal the expiration and eviction loops to start:
//   - The expirationTriggerCh channel is used to signal the expiration loop to start.
//   - The evictCh channel is used to signal the eviction loop to start.
//
// The cache also has a mutex that is used to protect the eviction algorithm from concurrent access.
// The stop channel is used to signal the expiration and eviction loops to stop. The evictCh channel is used to signal the eviction loop to start.
type HyperCache[T backend.IBackendConstrain] struct {
	backend               backend.IBackend[T]          // `backend`` holds the backend that the cache uses to store the items. It must implement the IBackend interface.
	cacheBackendChecker   utils.CacheBackendChecker[T] // `cacheBackendChecker` holds an instance of the CacheBackendChecker interface. It helps to determine the type of the backend.
	stop                  chan bool                    // `stop` channel to signal the expiration and eviction loops to stop
	workerPool            *WorkerPool                  // `workerPool` holds a pointer to the worker pool that the cache uses to run the expiration and eviction loops.
	expirationTriggerCh   chan bool                    // `expirationTriggerCh` channel to signal the expiration trigger loop to start
	evictCh               chan bool                    // `evictCh` channel to signal the eviction loop to start
	evictionAlgorithmName string                       // `evictionAlgorithmName` name of the eviction algorithm to use when evicting items
	evictionAlgorithm     eviction.IAlgorithm          // `evictionAlgorithm` eviction algorithm to use when evicting items
	expirationInterval    time.Duration                // `expirationInterval` interval at which the expiration loop should run
	evictionInterval      time.Duration                // interval at which the eviction loop should run
	shouldEvict           atomic.Bool                  // `shouldEvict` indicates whether the cache should evict items or not
	maxEvictionCount      uint                         // `evictionInterval` maximum number of items that can be evicted in a single eviction loop iteration
	maxCacheSize          int64                        // maxCacheSize instructs the cache not allocate more memory than this limit, value in MB, 0 means no limit
	memoryAllocation      atomic.Int64                 // memoryAllocation is the current memory allocation of the cache, value in bytes
	mutex                 sync.RWMutex                 // `mutex` holds a RWMutex (Read-Write Mutex) that is used to protect the eviction algorithm from concurrent access
	once                  sync.Once                    // `once` holds a Once struct that is used to ensure that the expiration and eviction loops are only started once
	statsCollectorName    string                       // `statsCollectorName` holds the name of the stats collector that the cache should use when collecting cache statistics
	// StatsCollector to collect cache statistics
	StatsCollector stats.ICollector
}

// NewInMemoryWithDefaults initializes a new HyperCache with the default configuration.
// The default configuration is:
//   - The eviction interval is set to 10 minutes.
//   - The eviction algorithm is set to LRU.
//   - The expiration interval is set to 30 minutes.
//   - The capacity of the in-memory backend is set to 0 items (no limitations) unless specified.
//   - The maximum cache size in bytes is set to 0 (no limitations).
func NewInMemoryWithDefaults(capacity int) (hyperCache *HyperCache[backend.InMemory], err error) {
	// Initialize the configuration
	config := NewConfig[backend.InMemory]("in-memory")
	// Set the default options
	config.HyperCacheOptions = []Option[backend.InMemory]{
		WithEvictionInterval[backend.InMemory](10 * time.Minute),
		WithEvictionAlgorithm[backend.InMemory]("lru"),
		WithExpirationInterval[backend.InMemory](30 * time.Minute),
	}

	// Set the in-memory backend options
	config.InMemoryOptions = []backend.Option[backend.InMemory]{
		backend.WithCapacity[backend.InMemory](capacity),
	}

	hcm := GetDefaultManager()

	// Initialize the cache
	hyperCache, err = New(hcm, config)
	if err != nil {
		return nil, err
	}
	return hyperCache, nil
}

// New initializes a new HyperCache with the given configuration.
// The default configuration is:
//   - The eviction interval is set to 10 minutes.
//   - The eviction algorithm is set to CAWOLFU.
//   - The expiration interval is set to 30 minutes.
//   - The stats collector is set to the HistogramStatsCollector stats collector.
func New[T backend.IBackendConstrain](bm *BackendManager, config *Config[T]) (hyperCache *HyperCache[T], err error) {
	// Get the backend constructor from the registry
	constructor, exists := bm.backendRegistry[config.BackendType]
	if !exists {
		return nil, fmt.Errorf("unknown backend type: %s", config.BackendType)
	}

	// Create the backend
	backendInstance, err := constructor.Create(config)
	if err != nil {
		return nil, err
	}

	// Check if the backend implements the IBackend interface
	backend, ok := backendInstance.(backend.IBackend[T])
	if !ok {
		return nil, errors.ErrInvalidBackendType
	}

	// Initialize the cache
	hyperCache = &HyperCache[T]{
		backend:            backend,
		workerPool:         NewWorkerPool(runtime.NumCPU()),
		stop:               make(chan bool, 2),
		expirationInterval: 30 * time.Minute,
		evictionInterval:   10 * time.Minute,
	}

	// Initialize the cache backend type checker
	hyperCache.cacheBackendChecker = utils.CacheBackendChecker[T]{
		Backend:     hyperCache.backend,
		BackendType: config.BackendType,
	}

	// Apply options
	ApplyHyperCacheOptions(hyperCache, config.HyperCacheOptions...)

	// evaluate if the cache should evict items proactively
	hyperCache.shouldEvict.Store(hyperCache.evictionInterval == 0 && hyperCache.backend.Capacity() > 0)

	// Set the max eviction count to the capacity if it is not set or is zero
	if hyperCache.maxEvictionCount == 0 {
		hyperCache.maxEvictionCount = uint(hyperCache.backend.Capacity())
	}

	// Initialize the eviction algorithm
	if hyperCache.evictionAlgorithmName == "" {
		// Use the default eviction algorithm if none is specified
		hyperCache.evictionAlgorithm, err = eviction.NewLRUAlgorithm(int(hyperCache.maxEvictionCount))
	} else {
		// Use the specified eviction algorithm
		hyperCache.evictionAlgorithm, err = eviction.NewEvictionAlgorithm(hyperCache.evictionAlgorithmName, int(hyperCache.maxEvictionCount))
	}
	if err != nil {
		return
	}

	// Initialize the stats collector
	if hyperCache.statsCollectorName == "" {
		// Use the default stats collector if none is specified
		hyperCache.StatsCollector = stats.NewHistogramStatsCollector()
	} else {
		// Use the specified stats collector
		hyperCache.StatsCollector, err = stats.NewCollector(hyperCache.statsCollectorName)
		if err != nil {
			return
		}
	}

	// If the capacity is less than zero, we return
	if hyperCache.backend.Capacity() < 0 {
		return nil, errors.ErrInvalidCapacity
	}

	// Initialize the expiration trigger channel with the buffer size set to half the capacity
	hyperCache.expirationTriggerCh = make(chan bool, hyperCache.backend.Capacity()/2)

	// Initialize the eviction channel with the buffer size set to half the capacity
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// Start expiration and eviction loops if capacity is greater than zero
	hyperCache.once.Do(func() {
		tick := time.NewTicker(hyperCache.expirationInterval)
		go func() {
			for {
				select {
				case <-tick.C:
					// trigger expiration
					hyperCache.expirationLoop(ctx)
				case <-hyperCache.expirationTriggerCh:
					// trigger expiration
					hyperCache.expirationLoop(ctx)
				case <-hyperCache.evictCh:
					// trigger eviction
					hyperCache.evictionLoop(ctx)
				case <-hyperCache.stop:
					// stop the loops
					return
				}
			}
		}()
		// Start eviction loop if eviction interval is greater than zero
		if hyperCache.evictionInterval > 0 {
			// Initialize the eviction channel with the buffer size set to half the capacity
			hyperCache.evictCh = make(chan bool, 1)
			// Start the eviction loop
			tick := time.NewTicker(hyperCache.evictionInterval)
			go func() {
				for {
					select {
					case <-tick.C:
						hyperCache.evictionLoop(ctx)
					case <-hyperCache.stop:
						return
					}
				}
			}()
		}
	})

	return
}

// expirationLoop is a function that runs in a separate goroutine and expires items in the cache based on their expiration duration.
func (hyperCache *HyperCache[T]) expirationLoop(ctx context.Context) {
	hyperCache.workerPool.Enqueue(func() error {
		hyperCache.StatsCollector.Incr("expiration_loop_count", 1)
		defer hyperCache.StatsCollector.Timing("expiration_loop_duration", time.Now().UnixNano())

		var (
			expiredCount int64
			items        []*types.Item
			err          error
		)

		// get all expired items
		items, err = hyperCache.List(context.TODO(),
			backend.WithSortBy(types.SortByExpiration.String()),
			backend.WithFilterFunc(func(item *types.Item) bool {
				return item.Expiration > 0 && time.Since(item.LastAccess) > item.Expiration
			}))
		if err != nil {
			return err
		}

		// iterate all expired items and remove them
		for _, item := range items {
			expiredCount++
			hyperCache.Remove(ctx, item.Key)
			types.ItemPool.Put(item)
			hyperCache.StatsCollector.Incr("item_expired_count", 1)
		}

		hyperCache.StatsCollector.Gauge("item_count", int64(hyperCache.backend.Count()))
		hyperCache.StatsCollector.Gauge("expired_item_count", expiredCount)

		return nil
	})
}

// evictionLoop is a function that runs in a separate goroutine and evicts items from the cache based on the cache's capacity and the max eviction count.
// The eviction is determined by the eviction algorithm.
func (hyperCache *HyperCache[T]) evictionLoop(ctx context.Context) {
	// Enqueue the eviction loop in the worker pool to avoid blocking the main goroutine if the eviction loop is slow
	hyperCache.workerPool.Enqueue(func() error {
		hyperCache.StatsCollector.Incr("eviction_loop_count", 1)
		defer hyperCache.StatsCollector.Timing("eviction_loop_duration", time.Now().UnixNano())
		var evictedCount int64

		for {
			if hyperCache.backend.Count() <= hyperCache.backend.Capacity() {
				break
			}

			if hyperCache.maxEvictionCount == uint(evictedCount) {
				break
			}

			key, ok := hyperCache.evictionAlgorithm.Evict()
			if !ok {
				// no more items to evict
				break
			}

			// remove the item from the cache
			hyperCache.Remove(ctx, key)
			evictedCount++
			hyperCache.StatsCollector.Incr("item_evicted_count", 1)
		}

		hyperCache.StatsCollector.Gauge("item_count", int64(hyperCache.backend.Count()))
		hyperCache.StatsCollector.Gauge("evicted_item_count", evictedCount)
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

	hyperCache.Remove(ctx, key)
	return key, true
}

// Set adds an item to the cache with the given key and value. If an item with the same key already exists, it updates the value of the existing item.
// If the expiration duration is greater than zero, the item will expire after the specified duration.
// If the capacity of the cache is reached, the cache will leverage the eviction algorithm proactively if the evictionInterval is zero. If not, the background process will take care of the eviction.
func (hyperCache *HyperCache[T]) Set(ctx context.Context, key string, value any, expiration time.Duration) error {
	// Create a new cache item and set its properties
	item := types.ItemPool.Get().(*types.Item)
	item.Key = key
	item.Value = value
	item.Expiration = expiration
	item.LastAccess = time.Now()

	hyperCache.mutex.Lock()
	defer hyperCache.mutex.Unlock()

	// Set the size of the item
	err := item.SetSize()
	if err != nil {
		return err
	}

	// check if adding this item will exceed the maxCacheSize
	hyperCache.memoryAllocation.Add(item.Size)
	if hyperCache.maxCacheSize > 0 && hyperCache.memoryAllocation.Load() > hyperCache.maxCacheSize {
		hyperCache.memoryAllocation.Add(-item.Size)
		return errors.ErrCacheFull
	}

	// Insert the item into the cache
	err = hyperCache.backend.Set(item)
	if err != nil {
		hyperCache.memoryAllocation.Add(-item.Size)
		types.ItemPool.Put(item)
		return err
	}

	// Set the item in the eviction algorithm
	hyperCache.evictionAlgorithm.Set(key, item.Value)

	// If the cache is at capacity, evict an item when the eviction interval is zero
	if hyperCache.shouldEvict.Load() && hyperCache.backend.Count() > hyperCache.backend.Capacity() {
		hyperCache.evictItem(ctx)
	}

	return nil
}

// Get retrieves the item with the given key from the cache returning the value and a boolean indicating if the item was found.
func (hyperCache *HyperCache[T]) Get(key string) (value any, ok bool) {
	item, ok := hyperCache.backend.Get(key)
	if !ok {
		return nil, false
	}

	// Check if the item has expired, if so, trigger the expiration loop
	if item.Expired() {
		go func() {
			types.ItemPool.Put(item)
			hyperCache.expirationTriggerCh <- true
		}()
		return nil, false
	}

	// Update the last access time and access count
	item.Touch()
	return item.Value, true
}

// GetWithInfo retrieves the item with the given key from the cache returning the `Item` object and a boolean indicating if the item was found.
func (hyperCache *HyperCache[T]) GetWithInfo(key string) (*types.Item, bool) {
	item, ok := hyperCache.backend.Get(key)
	// Check if the item has expired if it exists, if so, trigger the expiration loop
	if !ok {
		return nil, false
	}

	// Check if the item has expired, if so, trigger the expiration loop
	if item.Expired() {
		go func() {
			types.ItemPool.Put(item)
			hyperCache.expirationTriggerCh <- true
		}()
		return nil, false
	}

	// Update the last access time and access count
	item.Touch()
	return item, true
}

// GetOrSet retrieves the item with the given key. If the item is not found, it adds the item to the cache with the given value and expiration duration.
// If the capacity of the cache is reached, leverage the eviction algorithm.
func (hyperCache *HyperCache[T]) GetOrSet(ctx context.Context, key string, value any, expiration time.Duration) (any, error) {
	// if the item is found, return the value
	if item, ok := hyperCache.backend.Get(key); ok {

		// Check if the item has expired
		if item.Expired() {
			go func() {
				types.ItemPool.Put(item)
				hyperCache.expirationTriggerCh <- true
			}()
			return nil, errors.ErrKeyExpired
		}

		// Update the last access time and access count
		item.Touch()
		return item.Value, nil

	}

	// if the item is not found, add it to the cache
	item := types.ItemPool.Get().(*types.Item)
	item.Key = key
	item.Value = value
	item.Expiration = expiration
	item.LastAccess = time.Now()

	hyperCache.mutex.Lock()
	defer hyperCache.mutex.Unlock()

	// Set the size of the item
	err := item.SetSize()
	if err != nil {
		return nil, err
	}

	// check if adding this item will exceed the maxCacheSize
	hyperCache.memoryAllocation.Add(item.Size)
	if hyperCache.maxCacheSize > 0 && hyperCache.memoryAllocation.Load() > hyperCache.maxCacheSize {
		hyperCache.memoryAllocation.Add(-item.Size)
		types.ItemPool.Put(item)
		return nil, errors.ErrCacheFull
	}

	// Insert the item into the cache
	err = hyperCache.backend.Set(item)
	if err != nil {
		hyperCache.memoryAllocation.Add(-item.Size)
		types.ItemPool.Put(item)
		return nil, err
	}

	go func() {
		// Set the item in the eviction algorithm
		hyperCache.evictionAlgorithm.Set(key, item.Value)
		// If the cache is at capacity, evict an item when the eviction interval is zero
		if hyperCache.shouldEvict.Load() && hyperCache.backend.Count() > hyperCache.backend.Capacity() {
			types.ItemPool.Put(item)
			hyperCache.evictItem(ctx)
		}
	}()
	return value, nil
}

// GetMultiple retrieves the items with the given keys from the cache.
func (hyperCache *HyperCache[T]) GetMultiple(ctx context.Context, keys ...string) (result map[string]any, failed map[string]error) {
	result = make(map[string]any, len(keys))   // Preallocate the result map
	failed = make(map[string]error, len(keys)) // Preallocate the errors map

	for _, key := range keys {
		item, ok := hyperCache.backend.Get(key)
		if !ok {
			// Add the key to the errors map and continue
			failed[key] = errors.ErrKeyNotFound
			continue
		}

		// Check if the item has expired
		if item.Expired() {
			// Put the item back in the pool
			types.ItemPool.Put(item)
			// Add the key to the errors map
			failed[key] = errors.ErrKeyExpired
			// Trigger the expiration loop
			go hyperCache.expirationLoop(ctx)
		} else {
			item.Touch() // Update the last access time and access count
			// Add the item to the result map
			result[key] = item.Value
		}

	}

	return
}

// List lists the items in the cache that meet the specified criteria.
// It takes in a variadic number of any type as filters, it then checks the backend type, and calls the corresponding
// implementation of the List function for that backend, with the filters passed in as arguments
func (hyperCache *HyperCache[T]) List(ctx context.Context, filters ...backend.IFilter) ([]*types.Item, error) {
	return hyperCache.backend.List(ctx, filters...)
}

// Remove removes items with the given key from the cache. If an item is not found, it does nothing.
func (hyperCache *HyperCache[T]) Remove(ctx context.Context, keys ...string) {
	// Remove the item from the eviction algorithm
	// and update the memory allocation
	for _, key := range keys {
		item, ok := hyperCache.backend.Get(key)
		if ok {
			// remove the item from the cacheBackend and update the memory allocation
			hyperCache.memoryAllocation.Add(-item.Size)
			hyperCache.evictionAlgorithm.Delete(key)
		}
	}
	hyperCache.backend.Remove(ctx, keys...)
}

// Clear removes all items from the cache.
func (hyperCache *HyperCache[T]) Clear(ctx context.Context) error {
	var (
		items []*types.Item
		err   error
	)

	// get all expired items
	items, err = hyperCache.backend.List(context.TODO())
	if err != nil {
		return err
	}

	// clear the cacheBackend
	err = hyperCache.backend.Clear(ctx)
	if err != nil {
		return err
	}

	for _, item := range items {
		hyperCache.evictionAlgorithm.Delete(item.Key)
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
	if hyperCache.backend.Count() > hyperCache.Capacity() {
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
func (hyperCache *HyperCache[T]) Count() int {
	return hyperCache.backend.Count()
}

// TriggerEviction sends a signal to the eviction loop to start.
func (hyperCache *HyperCache[T]) TriggerEviction() {
	hyperCache.evictCh <- true
}

// Stop function stops the expiration and eviction loops and closes the stop channel.
func (hyperCache *HyperCache[T]) Stop() {
	// Stop the expiration and eviction loops
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		hyperCache.stop <- true
	}()
	wg.Wait()
	hyperCache.once = sync.Once{}
	hyperCache.workerPool.Shutdown()
}

// GetStats returns the stats collected by the cache.
func (hyperCache *HyperCache[T]) GetStats() stats.Stats {
	// Lock the cache's mutex to ensure thread-safety
	hyperCache.mutex.RLock()
	defer hyperCache.mutex.RUnlock()

	// Get the stats from the stats collector
	stats := hyperCache.StatsCollector.GetStats()

	return stats
}
