package hypercache

// Copyright 2023 F. All rights reserved.
// Use of this source code is governed by a Mozilla Public License 2.0
// license that can be found in the LICENSE file.
// HyperCache is an in-memory cache implementation in Go that supports the expiration and eviction of items.

import (
	"sync"
	"time"

	"github.com/hyp3rd/hypercache/backend"
	"github.com/hyp3rd/hypercache/cache"
	"github.com/hyp3rd/hypercache/errors"
	"github.com/hyp3rd/hypercache/stats"
	"github.com/hyp3rd/hypercache/types"
	"github.com/hyp3rd/hypercache/utils"
)

// HyperCache is an in-memory cache that stores items with a key and expiration duration.
// It has a custom ConcurrentMap to store the items in the cache,
// and a capacity field that limits the number of items that can be stored in the cache.
// The stop channel is used to signal the expiration and eviction loops to stop. The evictCh channel is used to signal the eviction loop to start.
type HyperCache[T backend.IBackendConstrain] struct {
	backend               backend.IBackend[T]              // backend to use to store the items in the cache
	cacheBackendChecker   utils.CacheBackendChecker[T]     // cache backend type checker
	stop                  chan bool                        // channel to signal the expiration and eviction loops to stop
	expirationTriggerCh   chan bool                        // channel to signal the expiration trigger loop to start
	evictCh               chan bool                        // channel to signal the eviction loop to start
	statsCollectorName    string                           // name of the stats collector to use when collecting cache statistics
	statsCollector        StatsCollector                   // stats collector to collect cache statistics
	evictionAlgorithmName string                           // name of the eviction algorithm to use when evicting items
	evictionAlgorithm     EvictionAlgorithm                // eviction algorithm to use when evicting items
	expirationInterval    time.Duration                    // interval at which the expiration loop should run
	evictionInterval      time.Duration                    // interval at which the eviction loop should run
	maxEvictionCount      uint                             // maximum number of items that can be evicted in a single eviction loop iteration
	SortBy                string                           // field to sort the items by
	SortAscending         bool                             // whether to sort the items in ascending order
	FilterFn              func(item *cache.CacheItem) bool // filter function to select items that meet certain criteria
	mutex                 sync.RWMutex                     // mutex to protect the eviction algorithm
	once                  sync.Once                        // used to ensure that the expiration and eviction loops are only started once
}

// NewHyperCache creates a new in-memory cache with the given capacity.
// If the capacity is negative, it returns an error.
// The function initializes the items map, and starts the expiration and eviction loops in separate goroutines.
func NewHyperCache[T backend.IBackendConstrain](capacity int, options ...Option[T]) (hyperCache *HyperCache[T], err error) {

	// Initialize the backend
	cacheBackend, err := backend.NewBackend[T]("memory", capacity)
	if err != nil {
		return nil, err
	}

	// Initialize the cache backend type checker
	checker := utils.CacheBackendChecker[T]{
		Backend: cacheBackend,
	}

	// Initialize the cache
	hyperCache = &HyperCache[T]{
		backend:             cacheBackend,
		cacheBackendChecker: checker,
		stop:                make(chan bool, 1),
		evictCh:             make(chan bool, 1),
		expirationInterval:  10 * time.Minute,
		evictionInterval:    1 * time.Minute,
		maxEvictionCount:    uint(capacity),
	}

	// Apply options
	for _, option := range options {
		option(hyperCache)
	}

	// Initialize the eviction algorithm
	if hyperCache.evictionAlgorithmName == "" {
		// Use the default eviction algorithm if none is specified
		hyperCache.evictionAlgorithm, err = NewCAWOLFU(int(hyperCache.maxEvictionCount))
	} else {
		// Use the specified eviction algorithm
		hyperCache.evictionAlgorithm, err = NewEvictionAlgorithm(hyperCache.evictionAlgorithmName, int(hyperCache.maxEvictionCount))
	}
	if err != nil {
		return
	}

	// Initialize the stats collector
	if hyperCache.statsCollectorName == "" {
		// Use the default stats collector if none is specified
		// hyperCache.statsCollector, err = NewStatsCollector("default")
		hyperCache.statsCollector = stats.NewHistogramStatsCollector()
	} else {
		// Use the specified stats collector
		hyperCache.statsCollector, err = NewStatsCollector(hyperCache.statsCollectorName)
		if err != nil {
			return
		}
	}

	// Initialize the expiration trigger channel with the buffer size set to half the capacity
	hyperCache.expirationTriggerCh = make(chan bool, capacity/2)

	if capacity < 0 {
		return
	}

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
					// stop the loop
					return
				}
			}
		}()
		// Start eviction loop if eviction interval is greater than zero
		if hyperCache.evictionInterval > 0 {
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
	hyperCache.statsCollector.Incr("expiration_loop_count", 1)
	defer hyperCache.statsCollector.Timing("expiration_loop_duration", time.Now().UnixNano())

	var (
		expiredCount int64
		items        []*cache.CacheItem
		err          error
	)

	// get all expired items
	if cb, ok := hyperCache.backend.(*backend.InMemoryBackend); ok {
		items, err = cb.List(
			backend.WithSortBy[backend.InMemoryBackend](types.SortByExpiration),
			backend.WithFilterFunc[backend.InMemoryBackend](func(item *cache.CacheItem) bool {
				return item.Expiration > 0 && time.Since(item.LastAccess) > item.Expiration
			}),
		)

	} else if cb, ok := hyperCache.backend.(*backend.RedisBackend); ok {
		items, err = cb.List(
			backend.WithSortBy[backend.RedisBackend](types.SortByExpiration),
			backend.WithFilterFunc[backend.RedisBackend](func(item *cache.CacheItem) bool {
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
		hyperCache.statsCollector.Incr("item_expired_count", 1)
	}

	hyperCache.statsCollector.Gauge("item_count", int64(hyperCache.backend.Size()))
	hyperCache.statsCollector.Gauge("expired_item_count", expiredCount)
}

// evictionLoop is a function that runs in a separate goroutine and evicts items from the cache based on the cache's capacity and the max eviction count.
func (hyperCache *HyperCache[T]) evictionLoop() {
	hyperCache.statsCollector.Incr("eviction_loop_count", 1)
	defer hyperCache.statsCollector.Timing("eviction_loop_duration", time.Now().UnixNano())
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
		hyperCache.statsCollector.Incr("item_evicted_count", 1)
	}

	hyperCache.statsCollector.Gauge("item_count", int64(hyperCache.backend.Size()))
	hyperCache.statsCollector.Gauge("evicted_item_count", evictedCount)
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
// If the capacity of the cache is reached, the cache will evict the least recently used item before adding the new item.
func (hyperCache *HyperCache[T]) Set(key string, value interface{}, expiration time.Duration) error {
	item := cache.CacheItemPool.Get().(*cache.CacheItem)
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

	// If the cache is at capacity, evict an item
	if hyperCache.evictionInterval == 0 && hyperCache.backend.Capacity() > 0 && hyperCache.backend.Size() > hyperCache.backend.Capacity() {
		hyperCache.evictItem()
	}

	return nil
}

// Get retrieves the item with the given key from the cache. If the item is not found, it returns nil.
func (hyperCache *HyperCache[T]) Get(key string) (value interface{}, ok bool) {
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

// GetOrSet retrieves the item with the given key from the hyperCache. If the item is not found, it adds the item to the cache with the given value and expiration duration.
// If the capacity of the cache is reached, the cache will evict the least recently used item before adding the new item.
func (hyperCache *HyperCache[T]) GetOrSet(key string, value interface{}, expiration time.Duration) (interface{}, error) {
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
	item := cache.CacheItemPool.Get().(*cache.CacheItem)
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

	hyperCache.evictionAlgorithm.Set(key, item.Value)
	if hyperCache.evictionInterval == 0 && hyperCache.Capacity() > 0 && hyperCache.Size() > hyperCache.Capacity() {
		hyperCache.evictItem()
	}

	return value, nil
}

// GetMultiple retrieves the items with the given keys from the cache. If an item is not found, it is not included in the returned map.
func (hyperCache *HyperCache[T]) GetMultiple(keys ...string) (result map[string]interface{}, failed map[string]error) {
	result = make(map[string]interface{}, len(keys)) // Preallocate the result map
	failed = make(map[string]error, len(keys))       // Preallocate the errors map

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
func (hyperCache *HyperCache[T]) List(filters ...any) ([]*cache.CacheItem, error) {
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
// a slice of CacheItem pointers, and an error
type listFunc func(options ...any) ([]*cache.CacheItem, error)

// listInMemory is a function that takes in an InMemoryBackend, and returns a ListFunc
// it takes any type as filters, and converts them to the specific FilterOption type for the InMemoryBackend,
// and calls the InMemoryBackend's List function with these filters.
func listInMemory(cacheBackend *backend.InMemoryBackend) listFunc {
	return func(options ...any) ([]*cache.CacheItem, error) {
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
		items []*cache.CacheItem
		err   error
	)

	// get all expired items
	if cb, ok := hyperCache.backend.(*backend.InMemoryBackend); ok {
		items, err = cb.List(
			backend.WithSortBy[backend.InMemoryBackend](types.SortByExpiration),
		)
		cb.Clear()
	} else if cb, ok := hyperCache.backend.(*backend.RedisBackend); ok {
		items, err = cb.List(
			backend.WithSortBy[backend.RedisBackend](types.SortByExpiration),
		)
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
func (cache *HyperCache[T]) TriggerEviction() {
	cache.evictCh <- true
}

// Stop function stops the expiration and eviction loops and closes the stop channel.
func (cache *HyperCache[T]) Stop() {
	// Stop the expiration and eviction loops
	cache.once = sync.Once{}
	cache.stop <- true
	close(cache.stop)
	close(cache.evictCh)
}

// GetStats returns the stats collected by the cache.
// It returns a map where the keys are the stat names and the values are the stat values.
func (cache *HyperCache[T]) GetStats() stats.Stats {
	// Lock the cache's mutex to ensure thread-safety
	cache.mutex.RLock()
	defer cache.mutex.RUnlock()

	stats := cache.statsCollector.GetStats()

	return stats
}
