// Copyright 2023 F. All rights reserved.
// Use of this source code is governed by a Mozilla Public License 2.0
// license that can be found in the LICENSE file.
// HyperCache is an in-memory cache implementation in Go that supports the expiration and eviction of items.
package hypercache

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/hyp3rd/hypercache/stats"
	"github.com/hyp3rd/hypercache/types"
)

// HyperCache is an in-memory cache that stores items with a key and expiration duration.
// It has a sync.Map items to store the items in the cache,
// and a capacity field that limits the number of items that can be stored in the cache.
// The stop channel is used to signal the expiration and eviction loops to stop. The evictCh channel is used to signal the eviction loop to start.
type HyperCache struct {
	items                     sync.Map                   // map to store the items in the cache
	capacity                  int                        // capacity of the cache, limits the number of items that can be stored in the cache
	stop                      chan bool                  // channel to signal the expiration and eviction loops to stop
	evictCh                   chan bool                  // channel to signal the eviction loop to start
	evictionTriggerCh         chan bool                  // channel to signal the eviction trigger loop to start
	once                      sync.Once                  // used to ensure that the expiration and eviction loops are only started once
	statsCollector            StatsCollector             // stats collector to collect cache statistics
	expirationInterval        time.Duration              // interval at which the expiration loop should run
	evictionInterval          time.Duration              // interval at which the eviction loop should run
	evictionTriggerBufferSize uint                       // size of the eviction trigger buffer
	maxEvictionCount          uint                       // maximum number of items that can be evicted in a single eviction loop iteration
	sortBy                    string                     // field to sort the items by
	sortAscending             bool                       // whether to sort the items in ascending order
	filterFn                  func(item *CacheItem) bool // filter function to select items that meet certain criteria
}

// StatsCollector is an interface that defines the methods that a stats collector should implement.
type StatsCollector interface {
	// Incr increments the count of a statistic by the given value.
	Incr(stat stats.Stat, value int64)
	// Decr decrements the count of a statistic by the given value.
	Decr(stat stats.Stat, value int64)
	// Timing records the time it took for an event to occur.
	Timing(stat stats.Stat, value int64)
	// Gauge records the current value of a statistic.
	Gauge(stat stats.Stat, value int64)
	// Histogram records the statistical distribution of a set of values.
	Histogram(stat stats.Stat, value int64)
}

// NewHyperCache creates a new in-memory cache with the given capacity.
// If the capacity is negative, it returns an error.
// The function initializes the items map, and starts the expiration and eviction loops in separate goroutines.
func NewHyperCache(capacity int, options ...Option) (cache *HyperCache, err error) {
	if capacity < 0 {
		return nil, fmt.Errorf("invalid capacity: %d", capacity)
	}

	cache = &HyperCache{
		items:              sync.Map{},
		capacity:           capacity,
		stop:               make(chan bool, 1),
		evictCh:            make(chan bool, 1),
		statsCollector:     stats.NewHistogramStatsCollector(), // initialize the default stats collector field
		expirationInterval: 30 * time.Minute,
		evictionInterval:   5 * time.Minute,
		maxEvictionCount:   10,
	}

	// Apply options
	for _, option := range options {
		option(cache)
	}

	if cache.evictionTriggerBufferSize == 0 {
		// The default eviction trigger buffer size should be large enough to prevent the channel from filling up under normal circumstances,
		// but not so large that it consumes too much memory.
		// The appropriate size will depend on the specific requirements of your application and the expected rate at which the eviction loop will be triggered.
		// As a general rule of thumb, you can start with a relatively small buffer size (e.g. 10-100) and increase it if you observe that the channel is filling up frequently.
		// Keep in mind that increasing the buffer size will increase the memory usage of the application, so you should be mindful of this when deciding on the buffer size.
		// It's also worth noting that if the eviction loop is able to keep up with the rate at which it is being triggered,
		// you may not need a large buffer size at all. In this case, you could set the buffer size to 1 to ensure that the eviction loop is triggered synchronously and to avoid the overhead of buffering messages.
		cache.evictionTriggerBufferSize = 10
	}

	cache.evictionTriggerCh = make(chan bool, cache.evictionTriggerBufferSize)

	// Start expiration and eviction loops if capacity is greater than zero
	if capacity > 0 {
		cache.once.Do(func() {
			tick := time.NewTicker(cache.expirationInterval)
			go func() {
				for {
					select {
					case <-tick.C:
						// trigger expiration
						cache.expirationLoop()
					case <-cache.evictCh:
						// trigger eviction
						cache.evictionLoop()
					case <-cache.evictionTriggerCh:
						// trigger eviction
						cache.evictItem()
					case <-cache.stop:
						// stop the loop
						return
					}
				}
			}()
			// Start eviction loop if eviction interval is greater than zero
			if cache.evictionInterval > 0 {
				tick := time.NewTicker(cache.evictionInterval)
				go func() {
					for {
						select {
						case <-tick.C:
							cache.evictionLoop()
						case <-cache.stop:
							return
						}
					}
				}()
			}
		})
	}

	return
}

// expirationLoop is a function that runs in a separate goroutine and expires items in the cache based on their expiration duration.
func (cache *HyperCache) expirationLoop() {
	cache.statsCollector.Incr("expiration_loop_count", 1)
	defer cache.statsCollector.Timing("expiration_loop_duration", time.Now().UnixNano())

	var expiredCount int64
	cache.items.Range(func(key, value interface{}) bool {
		item := value.(*CacheItem)
		if item.Expiration > 0 && time.Since(item.lastAccess) > item.Expiration {
			expiredCount++
			cache.items.Delete(key)
			cache.statsCollector.Incr("item_expired_count", 1)
		}
		return true
	})
	cache.statsCollector.Gauge("item_count", int64(cache.itemCount()))
	cache.statsCollector.Gauge("expired_item_count", expiredCount)
}

// evictionLoop is a function that runs in a separate goroutine and evicts items from the cache based on the cache's capacity and the max eviction count.
func (cache *HyperCache) evictionLoop() {
	cache.statsCollector.Incr("eviction_loop_count", 1)
	defer cache.statsCollector.Timing("eviction_loop_duration", time.Now().UnixNano())

	var evictedCount int64
	for i := uint(0); i < cache.maxEvictionCount; i++ {
		if cache.itemCount() <= cache.capacity {
			break
		}
		item, ok := cache.evictItem()
		if !ok {
			break
		}
		cache.items.Delete(item.Key)
		evictedCount++
		cache.statsCollector.Incr("item_evicted_count", 1)
	}
	cache.statsCollector.Gauge("item_count", int64(cache.itemCount()))
	cache.statsCollector.Gauge("evicted_item_count", evictedCount)
}

// evictItem is a helper function that returns the next item to be evicted from the cache.
func (cache *HyperCache) evictItem() (*CacheItem, bool) {
	var item *CacheItem
	var ok bool
	if cache.sortBy == "last_access" {
		item, ok = cache.evictItemByLastAccess()
	} else if cache.sortBy == "access_count" {
		item, ok = cache.evictItemByAccessCount()
	} else if cache.sortBy == "expiration" {
		item, ok = cache.evictItemByExpiration()
	} else {
		item, ok = cache.evictItemByLRU()
	}
	return item, ok
}

// evictItemByLastAccess is a helper function that returns the next item to be evicted from the cache based on the last access time of the items.
func (cache *HyperCache) evictItemByLastAccess() (*CacheItem, bool) {
	var evictionItem *CacheItem
	cache.items.Range(func(key, value interface{}) bool {
		item := value.(*CacheItem)
		if evictionItem == nil || item.lastAccess.Before(evictionItem.lastAccess) {
			evictionItem = item
		}
		return true
	})
	if evictionItem == nil {
		return nil, false
	}
	return evictionItem, true
}

// evictItemByAccessCount is a helper function that returns the next item to be evicted from the cache based on the access count of the items.
func (cache *HyperCache) evictItemByAccessCount() (*CacheItem, bool) {
	var evictionItem *CacheItem
	cache.items.Range(func(key, value interface{}) bool {
		item := value.(*CacheItem)
		if evictionItem == nil || item.accessCount < evictionItem.accessCount {
			evictionItem = item
		}
		return true
	})
	if evictionItem == nil {
		return nil, false
	}
	return evictionItem, true
}

// evictItemByExpiration is a helper function that returns the next item to be evicted from the cache based on the expiration duration of the items.
func (cache *HyperCache) evictItemByExpiration() (*CacheItem, bool) {
	var evictionItem *CacheItem
	cache.items.Range(func(key, value interface{}) bool {
		item := value.(*CacheItem)
		if evictionItem == nil || item.Expiration < evictionItem.Expiration {
			evictionItem = item
		}
		return true
	})
	if evictionItem == nil {
		return nil, false
	}
	return evictionItem, true
}

// evictItemByLRU is a helper function that returns the least recently used item in the cache.
func (cache *HyperCache) evictItemByLRU() (*CacheItem, bool) {
	var evictionItem *CacheItem
	cache.items.Range(func(key, value interface{}) bool {
		item := value.(*CacheItem)
		if evictionItem == nil || item.lastAccess.Before(evictionItem.lastAccess) {
			evictionItem = item
		}
		return true
	})
	if evictionItem == nil {
		return nil, false
	}
	return evictionItem, true
}

// SetCapacity sets the capacity of the cache. If the new capacity is smaller than the current number of items in the cache,
// it evicts the excess items from the cache.
func (cache *HyperCache) SetCapacity(capacity int) {
	if capacity < 0 {
		return
	}
	cache.capacity = capacity
	for cache.itemCount() > cache.capacity {
		_, ok := cache.evictItem()
		if !ok {
			break
		}
	}
}

// itemCount returns the number of items in the cache.
func (cache *HyperCache) itemCount() int {
	var count int
	cache.items.Range(func(key, value interface{}) bool {
		count++
		return true
	})
	return count
}

// Close stops the expiration and eviction loops and closes the stop channel.
func (cache *HyperCache) Close() {
	cache.stop <- true
	close(cache.stop)
}

// Set adds an item to the cache with the given key and value. If an item with the same key already exists, it updates the value of the existing item.
// If the expiration duration is greater than zero, the item will expire after the specified duration.
// If the capacity of the cache is reached, the cache will evict the least recently used item before adding the new item.
func (cache *HyperCache) Set(key string, value interface{}, expiration time.Duration) error {
	// Check for invalid key, value, or duration
	if key == "" {
		return fmt.Errorf("key cannot be empty")
	}
	if value == nil {
		return fmt.Errorf("value cannot be nil")
	}
	if expiration < 0 {
		return fmt.Errorf("expiration cannot be negative")
	}

	item := &CacheItem{
		Key:        key,
		Value:      value,
		Expiration: expiration,
		lastAccess: time.Now(),
	}
	cache.items.Store(key, item)
	if cache.capacity > 0 && cache.itemCount() > cache.capacity {
		// if elem, ok := cache.evictItem(); !ok {
		// 	return fmt.Errorf("error evicting item: %v", elem)
		// }
		select {
		case cache.evictionTriggerCh <- true:
			// eviction triggered
		default:
			// eviction trigger channel is full, unable to trigger eviction
		}
	}
	return nil
}

// Get retrieves the item with the given key from the cache. If the item is not found, it returns nil.
func (cache *HyperCache) Get(key string) (value interface{}, ok bool) {
	item, ok := cache.items.Load(key)
	if !ok {
		return nil, false
	}
	if i, ok := item.(*CacheItem); ok {
		i.lastAccess = time.Now()
		i.accessCount++
		return i.Value, true
	}
	return nil, false
}

// GetOrSet retrieves the item with the given key from the cache. If the item is not found, it adds the item to the cache with the given value and expiration duration.
// If the capacity of the cache is reached, the cache will evict the least recently used item before adding the new item.
func (cache *HyperCache) GetOrSet(key string, value interface{}, expiration time.Duration) (interface{}, error) {
	item, ok := cache.items.Load(key)
	if ok {
		if i, ok := item.(*CacheItem); ok {
			i.lastAccess = time.Now()
			i.accessCount++
			return i.Value, nil
		}
	}
	item = &CacheItem{
		Key:        key,
		Value:      value,
		Expiration: expiration,
		lastAccess: time.Now(),
	}
	cache.items.Store(key, item)
	if cache.capacity > 0 && cache.itemCount() > cache.capacity {
		select {
		case cache.evictionTriggerCh <- true:
			// eviction triggered
		default:
			// eviction trigger channel is full, unable to trigger eviction
		}
	}
	return value, nil
}

// GetMultiple retrieves the items with the given keys from the cache. If an item is not found, it is not included in the returned map.
func (cache *HyperCache) GetMultiple(keys ...string) map[string]interface{} {
	result := make(map[string]interface{})
	for _, key := range keys {
		item, ok := cache.items.Load(key)
		if !ok {
			continue
		}
		if i, ok := item.(*CacheItem); ok {
			i.lastAccess = time.Now()
			i.accessCount++
			result[key] = i.Value
		}
	}
	return result
}

// List lists the items in the cache that meet the specified criteria.
func (cache *HyperCache) List(options ...Option) ([]*CacheItem, error) {
	for _, option := range options {
		option(cache)
	}

	items := make([]*CacheItem, 0)
	cache.items.Range(func(key, value interface{}) bool {
		item := value.(*CacheItem)
		if cache.filterFn == nil || cache.filterFn(item) {
			items = append(items, item)
		}
		return true
	})

	if cache.sortBy == "" {
		return items, nil
	}

	sort.Slice(items, func(i, j int) bool {
		a := items[i].FieldByName(cache.sortBy)
		b := items[j].FieldByName(cache.sortBy)
		switch cache.sortBy {
		case types.SortByKey.String():
			if cache.sortAscending {
				return a.Interface().(string) < b.Interface().(string)
			}
			return a.Interface().(string) > b.Interface().(string)
		case types.SortByLastAccess.String():
			if cache.sortAscending {
				return a.Interface().(time.Time).Before(b.Interface().(time.Time))
			}
			return a.Interface().(time.Time).After(b.Interface().(time.Time))
		case types.SortByAccessCount.String():
			if cache.sortAscending {
				return a.Interface().(uint) < b.Interface().(uint)
			}
			return a.Interface().(uint) > b.Interface().(uint)
		case types.SortByExpiration.String():
			if cache.sortAscending {
				return a.Interface().(time.Duration) < b.Interface().(time.Duration)
			}
			return a.Interface().(time.Duration) > b.Interface().(time.Duration)
		default:
			return false
		}
	})

	return items, nil
}

// Remove removes the item with the given key from the cache. If the item is not found, it does nothing.
func (cache *HyperCache) Remove(key string) {
	cache.items.Delete(key)
}

// Clear removes all items from the cache.
func (cache *HyperCache) Clear() {
	cache.items.Range(func(key, value interface{}) bool {
		cache.items.Delete(key)
		return true
	})
}

// Capacity returns the capacity of the cache.
func (cache *HyperCache) Capacity() int {
	return cache.capacity
}

// Size returns the number of items in the cache.
func (cache *HyperCache) Size() int {
	size := 0
	cache.items.Range(func(key, value interface{}) bool {
		size++
		return true
	})
	return size
}

// TriggerEviction sends a signal to the eviction loop to start.
func (c *HyperCache) TriggerEviction() {
	c.evictCh <- true
}

// The Stop function stops the expiration and eviction loops and closes the stop channel.
func (c *HyperCache) Stop() {
	// Stop the expiration and eviction loops
	c.once = sync.Once{}
	c.stop <- true
	close(c.stop)
	close(c.evictCh)
}
