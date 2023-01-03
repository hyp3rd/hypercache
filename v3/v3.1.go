// Copyright 2023 F. All rights reserved.
// Use of this source code is governed by a Mozilla Public License 2.0
// license that can be found in the LICENSE file.
// HyperCache is an in-memory cache implementation in Go that supports the expiration and eviction of items.
package v3

// import (
// 	"errors"
// 	"sort"
// 	"sync"
// 	"time"

// 	"github.com/hyp3rd/hypercache/datastructure"
// 	"github.com/hyp3rd/hypercache/stats"
// 	"github.com/hyp3rd/hypercache/types"
// )

// var (
// 	// ErrInvalidKey is returned when an invalid key is used to access an item in the cache.
// 	// An invalid key is a key that is either empty or consists only of whitespace characters.
// 	ErrInvalidKey = errors.New("invalid key")

// 	// ErrKeyNotFound is returned when a key is not found in the cache.
// 	ErrKeyNotFound = errors.New("key not found")

// 	// ErrNilValue is returned when a nil value is attempted to be set in the cache.
// 	ErrNilValue = errors.New("nil value")

// 	// ErrKeyExpired is returned when a key is found in the cache but has expired.
// 	ErrKeyExpired = errors.New("key expired")

// 	// ErrInvalidExpiration is returned when an invalid expiration is passed to a cache item.
// 	ErrInvalidExpiration = errors.New("expiration cannot be negative")

// 	// ErrInvalidCapacity is returned when an invalid capacity is passed to the cache.
// 	ErrInvalidCapacity = errors.New("capacity cannot be negative")
// )

// // HyperCache is an in-memory cache that stores items with a key and expiration duration.
// // It has a sync.Map items to store the items in the cache,
// // and a capacity field that limits the number of items that can be stored in the cache.
// // The stop channel is used to signal the expiration and eviction loops to stop. The evictCh channel is used to signal the eviction loop to start.
// type HyperCache struct {
// 	// items                       sync.Map                   // map to store the items in the cache
// 	items                       datastructure.ConcurrentMap[string, *CacheItem] // map to store the items in the cache
// 	capacity                    int                                             // capacity of the cache, limits the number of items that can be stored in the cache
// 	stop                        chan bool                                       // channel to signal the expiration and eviction loops to stop
// 	expirationTriggerCh         chan bool                                       // channel to signal the expiration trigger loop to start
// 	evictCh                     chan bool                                       // channel to signal the eviction loop to start
// 	statsCollector              StatsCollector                                  // stats collector to collect cache statistics
// 	evictionAlgorithmName       string                                          // name of the eviction algorithm to use when evicting items
// 	evictionAlgorithm           EvictionAlgorithm                               // eviction algorithm to use when evicting items
// 	expirationInterval          time.Duration                                   // interval at which the expiration loop should run
// 	evictionInterval            time.Duration                                   // interval at which the eviction loop should run
// 	expirationTriggerBufferSize uint                                            // size of the expiration trigger buffer
// 	maxEvictionCount            uint                                            // maximum number of items that can be evicted in a single eviction loop iteration
// 	sortBy                      string                                          // field to sort the items by
// 	sortAscending               bool                                            // whether to sort the items in ascending order
// 	filterFn                    func(item *CacheItem) bool                      // filter function to select items that meet certain criteria
// 	mutex                       sync.RWMutex                                    // mutex to protect the eviction algorithm
// 	once                        sync.Once                                       // used to ensure that the expiration and eviction loops are only started once
// }

// // StatsCollector is an interface that defines the methods that a stats collector should implement.
// type StatsCollector interface {
// 	// Incr increments the count of a statistic by the given value.
// 	Incr(stat stats.Stat, value int64)
// 	// Decr decrements the count of a statistic by the given value.
// 	Decr(stat stats.Stat, value int64)
// 	// Timing records the time it took for an event to occur.
// 	Timing(stat stats.Stat, value int64)
// 	// Gauge records the current value of a statistic.
// 	Gauge(stat stats.Stat, value int64)
// 	// Histogram records the statistical distribution of a set of values.
// 	Histogram(stat stats.Stat, value int64)
// }

// // NewHyperCache creates a new in-memory cache with the given capacity.
// // If the capacity is negative, it returns an error.
// // The function initializes the items map, and starts the expiration and eviction loops in separate goroutines.
// func NewHyperCache(capacity int, options ...Option) (cache *HyperCache, err error) {
// 	if capacity < 0 {
// 		return nil, ErrInvalidCapacity
// 	}

// 	cache = &HyperCache{
// 		// items:              sync.Map{},
// 		items:              datastructure.New[*CacheItem](),
// 		capacity:           capacity,
// 		stop:               make(chan bool, 1),
// 		evictCh:            make(chan bool, 1),
// 		statsCollector:     stats.NewHistogramStatsCollector(), // initialize the default stats collector field
// 		expirationInterval: 10 * time.Minute,
// 		evictionInterval:   1 * time.Minute,
// 		maxEvictionCount:   100,
// 	}

// 	// Apply options
// 	for _, option := range options {
// 		option(cache)
// 	}

// 	// Initialize the eviction algorithm
// 	if cache.evictionAlgorithmName == "" {
// 		// Use the default eviction algorithm if none is specified
// 		cache.evictionAlgorithm, err = NewARC(capacity)
// 	} else {
// 		// Use the specified eviction algorithm
// 		cache.evictionAlgorithm, err = NewEvictionAlgorithm(cache.evictionAlgorithmName, capacity)
// 	}
// 	if err != nil {
// 		return nil, err
// 	}

// 	if cache.expirationTriggerBufferSize == 0 {
// 		cache.expirationTriggerBufferSize = 100
// 	}
// 	// Initialize the expiration trigger channel with the specified buffer size
// 	cache.expirationTriggerCh = make(chan bool, cache.expirationTriggerBufferSize)

// 	// Start expiration and eviction loops if capacity is greater than zero
// 	if capacity > 0 {
// 		cache.once.Do(func() {
// 			tick := time.NewTicker(cache.expirationInterval)
// 			go func() {
// 				for {
// 					select {
// 					case <-tick.C:
// 						// trigger expiration
// 						cache.expirationLoop()
// 					case <-cache.expirationTriggerCh:
// 						// trigger expiration
// 						cache.expirationLoop()
// 					case <-cache.evictCh:
// 						// trigger eviction
// 						cache.evictionLoop()
// 					case <-cache.stop:
// 						// stop the loop
// 						return
// 					}
// 				}
// 			}()
// 			// Start eviction loop if eviction interval is greater than zero
// 			if cache.evictionInterval > 0 {
// 				tick := time.NewTicker(cache.evictionInterval)
// 				go func() {
// 					for {
// 						select {
// 						case <-tick.C:
// 							cache.evictionLoop()
// 						case <-cache.stop:
// 							return
// 						}
// 					}
// 				}()
// 			}
// 		})
// 	}

// 	return
// }

// // expirationLoop is a function that runs in a separate goroutine and expires items in the cache based on their expiration duration.
// func (cache *HyperCache) expirationLoop() {
// 	cache.statsCollector.Incr("expiration_loop_count", 1)
// 	defer cache.statsCollector.Timing("expiration_loop_duration", time.Now().UnixNano())

// 	var expiredCount int64
// 	// cache.items.Range(func(key, value interface{}) bool {
// 	// 	item := value.(*CacheItem)
// 	// 	if item.Expiration > 0 && time.Since(item.lastAccess) > item.Expiration {
// 	// 		expiredCount++
// 	// 		cache.items.Delete(key)
// 	// 		cache.statsCollector.Incr("item_expired_count", 1)
// 	// 	}
// 	// 	return true
// 	// })

// 	for item := range cache.items.IterBuffered() {
// 		if item.Val.Expiration > 0 && time.Since(item.Val.lastAccess) > item.Val.Expiration {
// 			expiredCount++
// 			cache.items.Remove(item.Key)
// 			cache.statsCollector.Incr("item_expired_count", 1)
// 		}
// 	}

// 	cache.statsCollector.Gauge("item_count", int64(cache.itemCount()))
// 	cache.statsCollector.Gauge("expired_item_count", expiredCount)
// }

// // evictionLoop is a function that runs in a separate goroutine and evicts items from the cache based on the cache's capacity and the max eviction count.
// func (cache *HyperCache) evictionLoop() {
// 	// cache.statsCollector.Incr("eviction_loop_count", 1)
// 	// defer cache.statsCollector.Timing("eviction_loop_duration", time.Now().UnixNano())
// 	// var evictedCount int64
// 	for i := uint(0); i < cache.maxEvictionCount; i++ {
// 		if cache.itemCount() <= cache.capacity {
// 			break
// 		}
// 		key, err := cache.evictionAlgorithm.Evict()
// 		if err != nil {
// 			break
// 		}

// 		// defer cache.items.Delete(key)
// 		cache.items.Remove(key)
// 		// evictedCount++
// 		// cache.statsCollector.Incr("item_evicted_count", 1)
// 	}
// 	// cache.statsCollector.Gauge("item_count", int64(cache.itemCount()))
// 	// cache.statsCollector.Gauge("evicted_item_count", evictedCount)
// }

// // evictItem is a helper function that removes an item from the cache and returns the key of the evicted item.
// // If no item can be evicted, it returns an error.
// func (cache *HyperCache) evictItem() (string, error) {
// 	key, err := cache.evictionAlgorithm.Evict()
// 	if err != nil {
// 		return "", err
// 	}
// 	// cache.items.Delete(key)
// 	cache.items.Remove(key)
// 	return key, nil
// }

// // SetCapacity sets the capacity of the cache. If the new capacity is smaller than the current number of items in the cache,
// // it evicts the excess items from the cache.
// func (cache *HyperCache) SetCapacity(capacity int) {
// 	if capacity < 0 {
// 		return
// 	}

// 	cache.capacity = capacity
// 	if cache.itemCount() > cache.capacity {
// 		cache.evictionLoop()
// 	}
// }

// // itemCount returns the number of items in the cache.
// func (cache *HyperCache) itemCount() int {
// 	// var count int
// 	// cache.items.Range(func(key, value interface{}) bool {
// 	// 	count++
// 	// 	return true
// 	// })
// 	// return count
// 	return cache.items.Count()
// }

// // Close stops the expiration and eviction loops and closes the stop channel.
// func (cache *HyperCache) Close() {
// 	cache.stop <- true
// 	close(cache.stop)
// }

// // Set adds an item to the cache with the given key and value. If an item with the same key already exists, it updates the value of the existing item.
// // If the expiration duration is greater than zero, the item will expire after the specified duration.
// // If the capacity of the cache is reached, the cache will evict the least recently used item before adding the new item.
// func (cache *HyperCache) Set(key string, value interface{}, expiration time.Duration) error {
// 	item := &CacheItem{
// 		Value:      value,
// 		Expiration: expiration,
// 		lastAccess: time.Now(),
// 	}

// 	// Check for invalid key, value, or duration
// 	if err := item.Valid(); err != nil {
// 		return err
// 	}

// 	cache.mutex.Lock()
// 	defer cache.mutex.Unlock()
// 	// cache.items.Store(key, item)
// 	cache.items.Set(key, item)
// 	defer cache.evictionAlgorithm.Set(key, item.Value)
// 	if cache.capacity > 0 && cache.itemCount() > cache.capacity {
// 		defer cache.evictItem()
// 	}

// 	return nil
// }

// // Get retrieves the item with the given key from the cache. If the item is not found, it returns nil.
// func (cache *HyperCache) Get(key string) (value interface{}, ok bool) {
// 	// item, ok := cache.items.Load(key)
// 	item, ok := cache.items.Get(key)
// 	if !ok {
// 		return nil, false
// 	}

// 	// i, ok := item.(*CacheItem)
// 	// if ok {
// 	// 	if i.Expired() {
// 	// 		go func() {
// 	// 			cache.expirationTriggerCh <- true
// 	// 		}()
// 	// 		return nil, false
// 	// 	}

// 	// 	// Update the last access time and access count
// 	// 	defer i.Touch()
// 	// }
// 	if item.Expired() {
// 		go func() {
// 			cache.expirationTriggerCh <- true
// 		}()
// 		return nil, false
// 	}

// 	// Update the last access time and access count
// 	defer item.Touch()
// 	return item.Value, true
// }

// // GetOrSet retrieves the item with the given key from the cache. If the item is not found, it adds the item to the cache with the given value and expiration duration.
// // If the capacity of the cache is reached, the cache will evict the least recently used item before adding the new item.
// func (cache *HyperCache) GetOrSet(key string, value interface{}, expiration time.Duration) (interface{}, error) {
// 	// if the item is found, return the value
// 	// if item, ok := cache.items.Load(key); ok {
// 	// 	if i, ok := item.(*CacheItem); ok {
// 	// 		// Check if the item has expired
// 	// 		if i.Expired() {
// 	// 			go func() {
// 	// 				cache.expirationTriggerCh <- true
// 	// 			}()
// 	// 			return nil, ErrKeyExpired
// 	// 		}

// 	// 		// Update the last access time and access count
// 	// 		defer i.Touch()
// 	// 		return i.Value, nil
// 	// 	}
// 	// }

// 	if item, ok := cache.items.Get(key); ok {

// 		// Check if the item has expired
// 		if item.Expired() {
// 			go func() {
// 				cache.expirationTriggerCh <- true
// 			}()
// 			return nil, ErrKeyExpired
// 		}

// 		// Update the last access time and access count
// 		defer item.Touch()
// 		return item.Value, nil

// 	}

// 	// if the item is not found, add it to the cache
// 	item := &CacheItem{
// 		// Key:        key,
// 		Value:      value,
// 		Expiration: expiration,
// 		lastAccess: time.Now(),
// 	}

// 	// Check for invalid key, value, or duration
// 	if err := item.Valid(); err != nil {
// 		return nil, err
// 	}

// 	// cache.mutex.Lock()
// 	// defer cache.mutex.Unlock()
// 	// cache.items.Store(key, item)

// 	cache.mutex.Lock()
// 	defer cache.mutex.Unlock()
// 	cache.items.Set(key, item)

// 	defer cache.evictionAlgorithm.Set(key, item.Value)
// 	if cache.capacity > 0 && cache.itemCount() > cache.capacity {
// 		// cache.evictionTriggerCh <- true
// 		defer cache.evictItem()
// 		// <-cache.evictionTriggerCh
// 	}

// 	return value, nil
// }

// // GetMultiple retrieves the items with the given keys from the cache. If an item is not found, it is not included in the returned map.
// func (cache *HyperCache) GetMultiple(keys ...string) (result map[string]interface{}, errors []error) {
// 	// result = make(map[string]interface{})
// 	// for _, key := range keys {
// 	// 	item, ok := cache.items.Load(key)
// 	// 	if !ok {
// 	// 		errors = append(errors, ErrKeyNotFound)
// 	// 		continue
// 	// 	}
// 	// 	if i, ok := item.(*CacheItem); ok {
// 	// 		if i.Expired() {
// 	// 			errors = append(errors, ErrKeyExpired)
// 	// 			go cache.expirationLoop()
// 	// 		} else {
// 	// 			i.Touch() // Update the last access time and access count
// 	// 			result[key] = i.Value
// 	// 		}
// 	// 	}
// 	// }
// 	// return result, errors

// 	result = make(map[string]interface{})
// 	for _, key := range keys {
// 		item, ok := cache.items.Get(key)
// 		if !ok {
// 			errors = append(errors, ErrKeyNotFound)
// 			continue
// 		}

// 		if item.Expired() {
// 			errors = append(errors, ErrKeyExpired)
// 			go cache.expirationLoop()
// 		} else {
// 			item.Touch() // Update the last access time and access count
// 			result[key] = item.Value
// 		}

// 	}
// 	return result, errors

// }

// // List lists the items in the cache that meet the specified criteria.
// func (cache *HyperCache) List(options ...FilteringOption) ([]*CacheItem, error) {
// 	for _, option := range options {
// 		option(cache)
// 	}

// 	// items := make([]*CacheItem, 0)
// 	// cache.items.Range(func(key, value interface{}) bool {
// 	// 	item := value.(*CacheItem)
// 	// 	if cache.filterFn == nil || cache.filterFn(item) {
// 	// 		items = append(items, item)
// 	// 	}
// 	// 	return true
// 	// })

// 	// cache.items.Range(func(key, value interface{}) bool {
// 	// 	item := value.(*CacheItem)
// 	// 	if cache.filterFn == nil || cache.filterFn(item) {
// 	// 		items = append(items, item)
// 	// 	}
// 	// 	return true
// 	// })

// 	// for item := range cache.items.Items() {
// 	// 	if cache.filterFn == nil || cache.filterFn(item) {
// 	// 		items = append(items, item)
// 	// 	}
// 	// }

// 	items := make([]*CacheItem, 0)

// 	for item := range cache.items.IterBuffered() {
// 		if cache.filterFn == nil || cache.filterFn(item.Val) {
// 			items = append(items, item.Val)
// 		}
// 	}

// 	if cache.sortBy == "" {
// 		return items, nil
// 	}

// 	sort.Slice(items, func(i, j int) bool {
// 		a := items[i].FieldByName(cache.sortBy)
// 		b := items[j].FieldByName(cache.sortBy)
// 		switch cache.sortBy {
// 		case types.SortByValue.String():
// 			if cache.sortAscending {
// 				return a.Interface().(string) < b.Interface().(string)
// 			}
// 			return a.Interface().(string) > b.Interface().(string)
// 		case types.SortByLastAccess.String():
// 			if cache.sortAscending {
// 				return a.Interface().(time.Time).Before(b.Interface().(time.Time))
// 			}
// 			return a.Interface().(time.Time).After(b.Interface().(time.Time))
// 		case types.SortByAccessCount.String():
// 			if cache.sortAscending {
// 				return a.Interface().(uint) < b.Interface().(uint)
// 			}
// 			return a.Interface().(uint) > b.Interface().(uint)
// 		case types.SortByExpiration.String():
// 			if cache.sortAscending {
// 				return a.Interface().(time.Duration) < b.Interface().(time.Duration)
// 			}
// 			return a.Interface().(time.Duration) > b.Interface().(time.Duration)
// 		default:
// 			return false
// 		}
// 	})

// 	return items, nil
// }

// // Remove removes the items with the given key from the cache. If an item is not found, it does nothing.
// func (cache *HyperCache) Remove(keys ...string) {
// 	for _, key := range keys {
// 		cache.evictionAlgorithm.Delete(key)
// 		// cache.items.Delete(key)
// 		cache.items.Remove(key)
// 	}
// }

// // Clear removes all items from the cache.
// func (cache *HyperCache) Clear() {
// 	// cache.items.Range(func(key, value interface{}) bool {
// 	// 	cache.evictionAlgorithm.Delete(key.(string))
// 	// 	cache.items.Delete(key)
// 	// 	return true
// 	// })
// 	for item := range cache.items.IterBuffered() {
// 		cache.evictionAlgorithm.Delete(item.Key)
// 		cache.items.Remove(item.Key)
// 	}
// }

// // Capacity returns the capacity of the cache.
// func (cache *HyperCache) Capacity() int {
// 	return cache.capacity
// }

// // Size returns the number of items in the cache.
// func (cache *HyperCache) Size() int {
// 	// size := 0
// 	// cache.items.Range(func(key, value interface{}) bool {
// 	// 	size++
// 	// 	return true
// 	// })
// 	// return size
// 	return cache.itemCount()
// }

// // TriggerEviction sends a signal to the eviction loop to start.
// func (c *HyperCache) TriggerEviction() {
// 	c.evictCh <- true
// }

// // The Stop function stops the expiration and eviction loops and closes the stop channel.
// func (c *HyperCache) Stop() {
// 	// Stop the expiration and eviction loops
// 	c.once = sync.Once{}
// 	c.stop <- true
// 	close(c.stop)
// 	close(c.evictCh)
// }