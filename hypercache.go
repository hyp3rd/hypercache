package hypercache

// Copyright 2023 F. All rights reserved.
// Use of this source code is governed by a Mozilla Public License 2.0
// license that can be found in the LICENSE file.
// HyperCache is a cache implementation for Go that supports multiple backends with the expiration and eviction of items.
// It can be used as a standalone cache or as a cache middleware for a service.
// It can implement a service interface to interact with the cache with middleware support (default or custom).

import (
	"sync"
	"time"

	"github.com/hyp3rd/hypercache/backend"
	"github.com/hyp3rd/hypercache/cache"
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
// 1. The expiration loop runs every `expirationInterval` and checks for expired items.
// 2. The eviction loop runs every `evictionInterval` and evicts items using the eviction algorithm.
// The cache also has two channels that are used to signal the expiration and eviction loops to start:
// 1. The expirationTriggerCh channel is used to signal the expiration loop to start.
// 2. The evictCh channel is used to signal the eviction loop to start.
// The cache also has a mutex that is used to protect the eviction algorithm from concurrent access.
// The stop channel is used to signal the expiration and eviction loops to stop. The evictCh channel is used to signal the eviction loop to start.
type HyperCache[T backend.IBackendConstrain] struct {
	backend               backend.IBackend[T]          // `backend`` holds the backend that the cache uses to store the items. It must implement the IBackend interface.
	backendType           *T                           // `backendType`` holds a pointer to the backend type.
	cacheBackendChecker   utils.CacheBackendChecker[T] // `cacheBackendChecker` holds an instance of the CacheBackendChecker interface. It helps to determine the type of the backend.
	stop                  chan bool                    // `stop` channel to signal the expiration and eviction loops to stop
	expirationTriggerCh   chan bool                    // `expirationTriggerCh` channel to signal the expiration trigger loop to start
	evictCh               chan bool                    // `evictCh` channel to signal the eviction loop to start
	evictionAlgorithmName string                       // `evictionAlgorithmName` name of the eviction algorithm to use when evicting items
	evictionAlgorithm     eviction.Algorithm           // `evictionAlgorithm` eviction algorithm to use when evicting items
	expirationInterval    time.Duration                // `expirationInterval` interval at which the expiration loop should run
	evictionInterval      time.Duration                // interval at which the eviction loop should run
	maxEvictionCount      uint                         // `evictionInterval` maximum number of items that can be evicted in a single eviction loop iteration
	mutex                 sync.RWMutex                 // `mutex` holds a RWMutex (Read-Write Mutex) that is used to protect the eviction algorithm from concurrent access
	once                  sync.Once                    // `once` holds a Once struct that is used to ensure that the expiration and eviction loops are only started once
	statsCollectorName    string                       // `statsCollectorName` holds the name of the stats collector that the cache should use when collecting cache statistics
	// StatsCollector to collect cache statistics
	StatsCollector stats.StatsCollector
}

// NewHyperCacheInMemoryWithDefaults initializes a new HyperCache with the default configuration.
// The default configuration is:
// 1. The eviction interval is set to 10 minutes.
// 2. The eviction algorithm is set to LRU.
// 3. The expiration interval is set to 30 minutes.
// 4. The capacity of the in-memory backend is set to 1000 items.
func NewHyperCacheInMemoryWithDefaults(capacity int) (hyperCache *HyperCache[backend.InMemoryBackend], err error) {
	// Initialize the configuration
	config := NewConfig[backend.InMemoryBackend]()
	// Set the default options
	config.HyperCacheOptions = []Option[backend.InMemoryBackend]{
		WithEvictionInterval[backend.InMemoryBackend](10 * time.Minute),
		WithEvictionAlgorithm[backend.InMemoryBackend]("lru"),
		WithExpirationInterval[backend.InMemoryBackend](30 * time.Minute),
	}

	// Set the in-memory backend options
	config.InMemoryBackendOptions = []backend.BackendOption[backend.InMemoryBackend]{
		backend.WithCapacity(capacity),
	}
	// Initialize the cache
	hyperCache, err = NewHyperCache(config)
	if err != nil {
		return nil, err
	}
	return hyperCache, nil
}

// NewHyperCache initializes a new HyperCache with the given configuration.
// The default configuration is:
// 1. The eviction interval is set to 10 minutes.
// 2. The eviction algorithm is set to CAWOLFU.
// 3. The expiration interval is set to 30 minutes.
// 4. The stats collector is set to the HistogramStatsCollector stats collector.
func NewHyperCache[T backend.IBackendConstrain](config *Config[T]) (hyperCache *HyperCache[T], err error) {

	// Initialize the cache
	hyperCache = &HyperCache[T]{
		stop:               make(chan bool, 2),
		expirationInterval: 30 * time.Minute,
		evictionInterval:   10 * time.Minute,
	}

	// Initialize the backend
	t, _ := utils.TypeName(hyperCache.backendType) // Get the backend type name
	switch t {
	case "backend.InMemoryBackend":
		hyperCache.backend, err = backend.NewInMemoryBackend(config.InMemoryBackendOptions...)
	case "backend.RedisBackend":
		hyperCache.backend, err = backend.NewRedisBackend(config.RedisBackendOptions...)
	default:
		err = errors.ErrInvalidBackendType
	}
	// No, or invalid backend specified, we return
	if err != nil {
		return
	}

	// Initialize the cache backend type checker
	hyperCache.cacheBackendChecker = utils.CacheBackendChecker[T]{
		Backend: hyperCache.backend,
	}

	// Apply options
	ApplyHyperCacheOptions(hyperCache, config.HyperCacheOptions...)

	// Set the max eviction count to the capacity if it is not set or is zero
	if hyperCache.maxEvictionCount == 0 {
		hyperCache.maxEvictionCount = uint(hyperCache.backend.Capacity())
	}

	// Initialize the eviction algorithm
	if hyperCache.evictionAlgorithmName == "" {
		// Use the default eviction algorithm if none is specified
		hyperCache.evictionAlgorithm, err = eviction.NewCAWOLFU(int(hyperCache.maxEvictionCount))
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
		// hyperCache.statsCollector, err = NewStatsCollector("default")
		hyperCache.StatsCollector = stats.NewHistogramStatsCollector()
	} else {
		// Use the specified stats collector
		hyperCache.StatsCollector, err = stats.NewStatsCollector(hyperCache.statsCollectorName)
		if err != nil {
			return
		}
	}

	// If the capacity is less than zero, we return
	if hyperCache.backend.Capacity() < 0 {
		return
	}

	// Initialize the expiration trigger channel with the buffer size set to half the capacity
	hyperCache.expirationTriggerCh = make(chan bool, hyperCache.backend.Capacity()/2)

	// Start expiration and eviction loops if capacity is greater than zero
	hyperCache.once.Do(func() {
		tick := time.NewTicker(hyperCache.expirationInterval)
		go func() {
			for {
				select {
				case <-tick.C:
					// trigger expiration
					hyperCache.expirationLoop()
				case <-hyperCache.expirationTriggerCh:
					// trigger expiration
					hyperCache.expirationLoop()
				case <-hyperCache.evictCh:
					// trigger eviction
					hyperCache.evictionLoop()
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
						hyperCache.evictionLoop()
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
func (hyperCache *HyperCache[T]) expirationLoop() {
	hyperCache.StatsCollector.Incr("expiration_loop_count", 1)
	defer hyperCache.StatsCollector.Timing("expiration_loop_duration", time.Now().UnixNano())

	var (
		expiredCount int64
		items        []*cache.Item
		err          error
	)

	// get all expired items
	if cb, ok := hyperCache.backend.(*backend.InMemoryBackend); ok {
		items, err = cb.List(
			backend.WithSortBy[backend.InMemoryBackend](types.SortByExpiration),
			backend.WithFilterFunc[backend.InMemoryBackend](func(item *cache.Item) bool {
				return item.Expiration > 0 && time.Since(item.LastAccess) > item.Expiration
			}),
		)

	} else if cb, ok := hyperCache.backend.(*backend.RedisBackend); ok {
		items, err = cb.List(
			backend.WithSortBy[backend.RedisBackend](types.SortByExpiration),
			backend.WithFilterFunc[backend.RedisBackend](func(item *cache.Item) bool {
				return item.Expiration > 0 && time.Since(item.LastAccess) > item.Expiration
			}),
		)
	}

	// when error, return
	if err != nil {
		return
	}
	// iterate all expired items and remove them
	for _, item := range items {
		expiredCount++
		hyperCache.backend.Remove(item.Key)
		cache.CacheItemPool.Put(item)
		hyperCache.StatsCollector.Incr("item_expired_count", 1)
	}

	hyperCache.StatsCollector.Gauge("item_count", int64(hyperCache.backend.Size()))
	hyperCache.StatsCollector.Gauge("expired_item_count", expiredCount)
}

// evictionLoop is a function that runs in a separate goroutine and evicts items from the cache based on the cache's capacity and the max eviction count.
// The eviction is determined by the eviction algorithm.
func (hyperCache *HyperCache[T]) evictionLoop() {
	hyperCache.StatsCollector.Incr("eviction_loop_count", 1)
	defer hyperCache.StatsCollector.Timing("eviction_loop_duration", time.Now().UnixNano())
	var evictedCount int64

	for {
		if hyperCache.backend.Size() <= hyperCache.backend.Capacity() {
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

		hyperCache.backend.Remove(key)
		evictedCount++
		hyperCache.StatsCollector.Incr("item_evicted_count", 1)
	}

	hyperCache.StatsCollector.Gauge("item_count", int64(hyperCache.backend.Size()))
	hyperCache.StatsCollector.Gauge("evicted_item_count", evictedCount)
}

// evictItem is a helper function that removes an item from the cache and returns the key of the evicted item.
// If no item can be evicted, it returns a false.
func (hyperCache *HyperCache[T]) evictItem() (string, bool) {
	key, ok := hyperCache.evictionAlgorithm.Evict()
	if !ok {
		return "", false
	}

	hyperCache.backend.Remove(key)
	return key, true
}

// SetCapacity sets the capacity of the cache. If the new capacity is smaller than the current number of items in the cache,
// it evicts the excess items from the cache.
func (hyperCache *HyperCache[T]) SetCapacity(capacity int) {
	// set capacity of the backend
	hyperCache.backend.SetCapacity(capacity)
	// if the cache size is greater than the new capacity, evict items
	if hyperCache.backend.Size() > hyperCache.Capacity() {
		hyperCache.evictionLoop()
	}
}

// Set adds an item to the cache with the given key and value. If an item with the same key already exists, it updates the value of the existing item.
// If the expiration duration is greater than zero, the item will expire after the specified duration.
// If the capacity of the cache is reached, the cache will leverage the eviction algorithm proactively if the evictionInterval is zero. If not, the background process will take care of the eviction.
func (hyperCache *HyperCache[T]) Set(key string, value any, expiration time.Duration) error {
	// Create a new cache item and set its properties
	item := cache.CacheItemPool.Get().(*cache.Item)
	item.Key = key
	item.Value = value
	item.Expiration = expiration
	item.LastAccess = time.Now()

	hyperCache.mutex.Lock()
	defer hyperCache.mutex.Unlock()

	// Insert the item into the cache
	err := hyperCache.backend.Set(item)
	if err != nil {
		cache.CacheItemPool.Put(item)
		return err
	}

	// Set the item in the eviction algorithm
	hyperCache.evictionAlgorithm.Set(key, item.Value)

	// If the cache is at capacity, evict an item when the eviction interval is zero
	if hyperCache.evictionInterval == 0 && hyperCache.backend.Capacity() > 0 && hyperCache.backend.Size() > hyperCache.backend.Capacity() {
		hyperCache.evictItem()
	}

	return nil
}

func (hyperCache *HyperCache[T]) SetMultiple(items map[string]any, expiration time.Duration) error {
	// Create a new cache item and set its properties
	cacheItems := make([]*cache.Item, 0, len(items))
	for key, value := range items {
		item := cache.CacheItemPool.Get().(*cache.Item)
		item.Key = key
		item.Value = value
		item.Expiration = expiration
		item.LastAccess = time.Now()
		cacheItems = append(cacheItems, item)
	}

	hyperCache.mutex.Lock()
	defer hyperCache.mutex.Unlock()

	// Insert the items into the cache
	for _, item := range cacheItems {
		err := hyperCache.backend.Set(item)
		if err != nil {
			for _, item := range cacheItems {
				cache.CacheItemPool.Put(item)
			}
			return err
		}
		// Set the item in the eviction algorithm
		hyperCache.evictionAlgorithm.Set(item.Key, item.Value)
	}

	// If the cache is at capacity, evict an item when the eviction interval is zero
	if hyperCache.evictionInterval == 0 && hyperCache.backend.Capacity() > 0 && hyperCache.backend.Size() > hyperCache.backend.Capacity() {
		hyperCache.evictionLoop()
	}

	return nil
}

// Get retrieves the item with the given key from the cache.
func (hyperCache *HyperCache[T]) Get(key string) (value any, ok bool) {
	item, ok := hyperCache.backend.Get(key)
	if !ok {
		return nil, false
	}

	// Check if the item has expired, if so, trigger the expiration loop
	if item.Expired() {
		go func() {
			cache.CacheItemPool.Put(item)
			hyperCache.expirationTriggerCh <- true
		}()
		return nil, false
	}

	// Update the last access time and access count
	item.Touch()
	return item.Value, true
}

// GetOrSet retrieves the item with the given key. If the item is not found, it adds the item to the cache with the given value and expiration duration.
// If the capacity of the cache is reached, leverage the eviction algorithm.
func (hyperCache *HyperCache[T]) GetOrSet(key string, value any, expiration time.Duration) (any, error) {
	// if the item is found, return the value
	if item, ok := hyperCache.backend.Get(key); ok {

		// Check if the item has expired
		if item.Expired() {
			go func() {
				cache.CacheItemPool.Put(item)
				hyperCache.expirationTriggerCh <- true
			}()
			return nil, errors.ErrKeyExpired
		}

		// Update the last access time and access count
		item.Touch()
		return item.Value, nil

	}

	// if the item is not found, add it to the cache
	item := cache.CacheItemPool.Get().(*cache.Item)
	item.Key = key
	item.Value = value
	item.Expiration = expiration
	item.LastAccess = time.Now()

	// Check for invalid key, value, or duration
	if err := item.Valid(); err != nil {
		cache.CacheItemPool.Put(item)
		return nil, err
	}

	hyperCache.mutex.Lock()
	defer hyperCache.mutex.Unlock()
	err := hyperCache.backend.Set(item)
	if err != nil {
		cache.CacheItemPool.Put(item)
		return nil, err
	}

	go func() {
		// Set the item in the eviction algorithm
		hyperCache.evictionAlgorithm.Set(key, item.Value)
		// If the cache is at capacity, evict an item when the eviction interval is zero
		if hyperCache.evictionInterval == 0 && hyperCache.Capacity() > 0 && hyperCache.Size() > hyperCache.Capacity() {
			cache.CacheItemPool.Put(item)
			hyperCache.evictItem()
		}
	}()
	return value, nil
}

// GetMultiple retrieves the items with the given keys from the cache.
func (hyperCache *HyperCache[T]) GetMultiple(keys ...string) (result map[string]any, failed map[string]error) {
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
			cache.CacheItemPool.Put(item)
			// Add the key to the errors map
			failed[key] = errors.ErrKeyExpired
			// Trigger the expiration loop
			go hyperCache.expirationLoop()
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
func (hyperCache *HyperCache[T]) List(filters ...any) ([]*cache.Item, error) {
	var listInstance listFunc

	// checking the backend type
	if hyperCache.cacheBackendChecker.IsInMemoryBackend() {
		// if the backend is an InMemoryBackend, we set the listFunc to the ListInMemory function
		listInstance = listInMemory(hyperCache.backend.(*backend.InMemoryBackend))
	}

	// calling the corresponding implementation of the list function
	return listInstance(filters...)
}

// listFunc is a type that defines a function that takes in a variable number of any type as arguments, and returns
// a slice of Item pointers, and an error
type listFunc func(options ...any) ([]*cache.Item, error)

// listInMemory is a function that takes in an InMemoryBackend, and returns a ListFunc
// it takes any type as filters, and converts them to the specific FilterOption type for the InMemoryBackend,
// and calls the InMemoryBackend's List function with these filters.
func listInMemory(cacheBackend *backend.InMemoryBackend) listFunc {
	return func(options ...any) ([]*cache.Item, error) {
		// here we are converting the filters of any type to the specific FilterOption type for the InMemoryBackend
		filterOptions := make([]backend.FilterOption[backend.InMemoryBackend], len(options))
		for i, option := range options {
			filterOptions[i] = option.(backend.FilterOption[backend.InMemoryBackend])
		}
		return cacheBackend.List(filterOptions...)
	}
}

// Remove removes items with the given key from the cache. If an item is not found, it does nothing.
func (hyperCache *HyperCache[T]) Remove(keys ...string) {
	hyperCache.backend.Remove(keys...)
	for _, key := range keys {
		hyperCache.evictionAlgorithm.Delete(key)
	}
}

// Clear removes all items from the cache.
func (hyperCache *HyperCache[T]) Clear() error {
	var (
		items []*cache.Item
		err   error
	)

	// get all expired items
	if cb, ok := hyperCache.backend.(*backend.InMemoryBackend); ok {
		items, err = cb.List()
		cb.Clear()
	} else if cb, ok := hyperCache.backend.(*backend.RedisBackend); ok {
		items, err = cb.List()
		if err != nil {
			return err
		}
		err = cb.Clear()
	}

	for _, item := range items {
		hyperCache.evictionAlgorithm.Delete(item.Key)
	}
	return err
}

// Capacity returns the capacity of the cache.
func (hyperCache *HyperCache[T]) Capacity() int {
	return hyperCache.backend.Capacity()
}

// Size returns the number of items in the cache.
func (hyperCache *HyperCache[T]) Size() int {
	return hyperCache.backend.Size()
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
