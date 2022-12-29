// Copyright 2023 F. All rights reserved.
// Use of this source code is governed by a Mozilla Public License 2.0
// license that can be found in the LICENSE file.
// HyperCache is an in-memory cache implementation in Go that supports the expiration and eviction of items.
package hypercache

import (
	"container/list"
	"fmt"
	"sync"
	"time"

	"github.com/hyp3rd/hypercache/stats"
)

// CacheItem represents an item in the cache. It stores the key, value, last accessed time, and duration.
type CacheItem struct {
	Key                string                              // Key is the key of the item
	Value              interface{}                         // Value is the value stored in the cache
	LastAccessedBefore time.Time                           // LastAccessedBefore is the time before which the item was last accessed
	Duration           time.Duration                       // Duration is the time after which the item expires
	OnItemExpired      func(key string, value interface{}) // callback function to be executed when the item expires
}

// HyperCache is an in-memory cache that stores items with a key and expiration duration.
// It has a mutex mu to protect concurrent access to the cache,
// a doubly-linked list lru to store the items in the cache, a map itemsByKey to quickly look up items by their key,
// and a capacity field that limits the number of items that can be stored in the cache.
// The stop channel is used to signal the expiration and eviction loops to stop. The evictCh channel is used to signal the eviction loop to start.
type HyperCache struct {
	mu                 sync.RWMutex             // mutex to protect concurrent access to the cache
	lru                *list.List               // doubly-linked list to store the items in the cache
	itemsByKey         map[string]*list.Element // map to quickly look up items by their key
	capacity           int                      // capacity of the cache, limits the number of items that can be stored in the cache
	stop               chan bool                // channel to signal the expiration and eviction loops to stop
	evictCh            chan bool                // channel to signal the eviction loop to start
	once               sync.Once                // used to ensure that the expiration and eviction loops are only started once
	statsCollector     StatsCollector           // stats collector to collect cache statistics
	expirationInterval time.Duration            // interval at which the expiration loop should run
	evictionInterval   time.Duration            // interval at which the eviction loop should run
}

// NewHyperCache creates a new in-memory cache with the given capacity.
// If the capacity is negative, it returns an error.
// The function initializes the lru list and itemsByKey map, and starts the expiration and eviction loops in separate goroutines.
func NewHyperCache(capacity int, options ...Option) (cache *HyperCache, err error) {
	if capacity < 0 {
		return nil, fmt.Errorf("invalid capacity: %d", capacity)
	}

	cache = &HyperCache{
		lru:                list.New(),
		itemsByKey:         make(map[string]*list.Element),
		capacity:           capacity,
		stop:               make(chan bool, 1),
		evictCh:            make(chan bool, 1),
		statsCollector:     stats.NewCollector(), // initialize the default stats collector field
		expirationInterval: 30 * time.Minute,
		evictionInterval:   5 * time.Minute,
	}

	// Apply options
	for _, option := range options {
		option(cache)
	}

	// Start expiration and eviction loops if capacity is greater than zero
	if capacity > 0 {
		cache.once.Do(func() {
			tick := time.NewTicker(cache.expirationInterval)
			go func() {
				for {
					select {
					case <-tick.C:
						cache.expirationLoop()
					case <-cache.evictCh:
						cache.evictionLoop()
					case <-cache.stop:
						return
					}
				}
			}()
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

// SetStatsCollector sets the statsCollector field of the HyperCache struct to the given Collector.
// This allows the HyperCache to increment the appropriate counters in the Collector when cache events occur.
// The Collector is used to collect cache statistics, such as the number of cache hits, misses, evictions, and expirations.
// This function takes a pointer to a Collector as an argument, so that the original Collector can be modified.
// If a different type of StatsCollector is needed, the statsCollector field of the HyperCache struct can be set to a different type that implements the StatsCollector interface.
func (c *HyperCache) SetStatsCollector(collector *stats.Collector) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statsCollector = collector
}

// Add adds a cache item to the HyperCache.
// If the item already exists, it updates the value, duration, and last accessed time of the item.
// If the cache is at capacity, it evicts the least recently used item before adding the new item.
// If the key or value of the item is empty, or the duration is negative, it returns an error.
func (c *HyperCache) Add(cacheItem CacheItem) error {
	// Check for invalid key, value, or duration
	if cacheItem.Key == "" {
		return fmt.Errorf("key cannot be empty")
	}
	if cacheItem.Value == nil {
		return fmt.Errorf("value cannot be nil")
	}
	if cacheItem.Duration < 0 {
		return fmt.Errorf("invalid duration: %d", cacheItem.Duration)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Check if the item already exists
	if elem, ok := c.itemsByKey[cacheItem.Key]; ok {
		c.lru.MoveToFront(elem)
		item := elem.Value.(*CacheItem)
		// Update the value, duration, and last accessed time
		item.Value = cacheItem.Value
		item.LastAccessedBefore = time.Now()
		item.Duration = cacheItem.Duration
		item.OnItemExpired = cacheItem.OnItemExpired
		// Move the item to the front of the lru list
		c.lru.MoveToFront(elem)
		go c.statsCollector.IncrementHits() // increment hits in stats collector
		return nil
	}

	var wg sync.WaitGroup
	if c.lru.Len() >= c.capacity {
		// Evict the least recently used item
		select {
		case c.evictCh <- true:
			// Wait for eviction to complete
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-c.evictCh
			}()

			go func() {
				wg.Wait()
				c.statsCollector.IncrementEvictions()
			}()
		case <-c.stop:
			return nil
		}
	}

	// Create a new item and add it to the front of the lru list and itemsByKey map
	c.itemsByKey[cacheItem.Key] = c.lru.PushFront(&cacheItem)
	// c.itemsByKey[key] = ee
	go c.statsCollector.IncrementMisses() // increment misses in stats collector
	return nil
}

// Set adds a value to the cache with the given key and duration.
// If the key is an empty string, the value is nil, or the duration is negative, it returns an error.
// The function acquires a lock and checks if the item already exists in the cache.
// If it does, it updates the value, duration, and the last accessed time, moves the item to the front of the lru list, and releases the lock.
// If the item doesn't exist, the function checks if the cache is at capacity and evicts the least recently used item if necessary.
// Finally, it creates a new item and adds it to the front of the lru list and the itemsByKey map.
func (c *HyperCache) Set(key string, value interface{}, duration time.Duration) error {
	// Check for invalid key, value, or duration
	if key == "" {
		return fmt.Errorf("key cannot be empty")
	}
	if value == nil {
		return fmt.Errorf("value cannot be nil")
	}
	if duration < 0 {
		return fmt.Errorf("invalid duration: %s", duration)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Check if the item already exists
	if elem, ok := c.itemsByKey[key]; ok {
		c.lru.MoveToFront(elem)
		item := elem.Value.(*CacheItem)
		// Update the value, duration, and last accessed time
		item.Value = value
		item.LastAccessedBefore = time.Now()
		item.Duration = duration
		// Move the item to the front of the lru list
		c.lru.MoveToFront(elem)
		go c.statsCollector.IncrementHits() // increment hits in stats collector
		return nil
	}

	var wg sync.WaitGroup
	if c.lru.Len() >= c.capacity {
		// Evict the least recently used item
		select {
		case c.evictCh <- true:
			// Wait for eviction to complete
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-c.evictCh
			}()

			go func() {
				wg.Wait()
				c.statsCollector.IncrementEvictions()
			}()
		case <-c.stop:
			return nil
		}
	}

	// Check if the cache is at capacity
	// if c.lru.Len() >= c.capacity {
	// 	// Signal the eviction loop to start
	// 	c.evictCh <- true
	// }

	// Create a new item and add it to the front of the lru list and itemsByKey map
	c.itemsByKey[key] = c.lru.PushFront(&CacheItem{
		Key:                key,
		Value:              value,
		LastAccessedBefore: time.Now(),
		Duration:           duration,
	})
	// c.itemsByKey[key] = ee
	go c.statsCollector.IncrementMisses() // increment misses in stats collector
	return nil
}

// Get retrieves the value with the given key from the cache.
// If the key is an empty string or the item doesn't exist, it returns a nil value and an error.
// The function acquires a lock, updates the last accessed time of the item, moves the item to the front of the lru list,
// and releases the lock.
func (c *HyperCache) Get(key string) (value interface{}, ok bool) {
	if key == "" {
		return nil, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	// Check if the item exists
	if ee, ok := c.itemsByKey[key]; ok {
		item := ee.Value.(*CacheItem)
		// Check if the item has expired
		if time.Since(item.LastAccessedBefore) > item.Duration {
			return nil, false
		}
		// Update the last accessed time
		item.LastAccessedBefore = time.Now()
		// Move the item to the front of the lru list
		c.lru.MoveToFront(ee)
		go c.statsCollector.IncrementHits() // increment hits in stats collector
		return item.Value, true
	}

	go c.statsCollector.IncrementMisses() // increment misses in stats collector
	return nil, false
}

// Delete removes the item with the given key from the cache.
// If the key is an empty string or the item doesn't exist, it returns an error.
// The function acquires a lock, removes the item from the lru list and itemsByKey map, and releases the lock.
func (c *HyperCache) Delete(key string) error {
	if key == "" {
		return fmt.Errorf("key cannot be empty")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Check if the item exists
	if ee, ok := c.itemsByKey[key]; ok {
		// Remove the item from the lru list and itemsByKey map
		c.lru.Remove(ee)
		delete(c.itemsByKey, key)
		return nil
	}

	return fmt.Errorf("item with key %q not found", key)
}

// List returns a slice of all items in the cache.
// It acquires a lock, iterates through the lru list, and appends each item to the slice.
// If the lru list is empty, it returns an empty slice.
// The function releases the lock before returning the slice.
func (c *HyperCache) List() []*CacheItem {
	c.mu.RLock()
	defer c.mu.RUnlock()

	items := make([]*CacheItem, 0, c.lru.Len())

	// Iterate through the lru list and append each item to the slice
	for e := c.lru.Front(); e != nil; e = e.Next() {
		items = append(items, e.Value.(*CacheItem))
	}

	return items
}

// Clear removes all items from the cache.
// The function acquires a lock, removes all items from the lru list and itemsByKey map, and releases the lock.
func (c *HyperCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Remove all items from the lru list and itemsByKey map
	c.lru = list.New()
	c.lru.Init()
	c.itemsByKey = make(map[string]*list.Element)
}

// Len returns the number of items in the cache.
func (c *HyperCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.lru.Len()
}

// The Capacity function returns the capacity of the cache.
func (c *HyperCache) Capacity() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.capacity
}

// Stats returns the statistics collected by the stats collector.
func (c *HyperCache) Stats() any {
	return c.statsCollector.GetStats()
}

// SetCapacity sets the capacity of the cache. If the capacity is negative, it returns an error.
// The function acquires a lock, updates the capacity field, and releases the lock.
func (c *HyperCache) SetCapacity(capacity int) error {
	if capacity < 0 {
		return fmt.Errorf("invalid capacity: %d", capacity)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.capacity = capacity

	return nil
}

// The Stop function stops the expiration and eviction loops and closes the stop channel.
// The function acquires a lock and sends a value to the stop and evictCh channels.
func (c *HyperCache) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.once = sync.Once{}
	c.stop <- true
	close(c.stop)
	close(c.evictCh)
}

// Close stops the expiration and eviction loops and cleans the cache.
// The function calls the Stop method to stop the loops and cleans the cache.
func (c *HyperCache) Close() {
	// Stop the expiration and eviction loops
	c.Stop()

	// Clear the cache
	c.Clear()
}

// TriggerEviction sends a signal to the eviction loop to start.
func (c *HyperCache) TriggerEviction() {
	c.evictCh <- true
}

// The evictionLoop function removes the least recently used items from the cache until it is below the capacity.
// The function acquires a lock and iterates over the items in the lru list, starting from the back.
// It removes the item from the lru list and the itemsByKey map if the cache is at capacity.
// evictionLoop evicts the least recently used items from the cache until the size of the cache is below the capacity.
func (c *HyperCache) evictionLoop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Continue removing the least recently used items until the cache is below capacity
	for c.lru.Len() >= c.capacity {
		// Evict the least recently used item from the lru list and itemsByKey map until the cache size is below capacity
		ee := c.lru.Back()
		if ee == nil {
			break
		}
		c.removeElement(ee)
		c.statsCollector.IncrementEvictions() // increment evictions in stats collector
	}

	// Signal that eviction is complete
	c.evictCh <- false
}

// // expirationLoop is a goroutine that runs every minute and removes expired items from the cache.
// func (c *HyperCache) expirationLoop() {
// 	c.mu.Lock()
// 	defer c.mu.Unlock()

// 	now := time.Now()
// 	for ee := c.lru.Back(); ee != nil; ee = ee.Prev() {
// 		i := ee.Value.(*CacheItem)
// 		if i.lastAccessedBefore.Add(i.duration).Before(now) {
// 			c.removeElement(ee)
// 			c.statsCollector.IncrementExpirations() // increment expirations in stats collector
// 		}
// 	}
// }

// expirationLoop is a loop that runs at the expiration interval and removes expired items from the cache.
func (c *HyperCache) expirationLoop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for e := c.lru.Back(); e != nil; e = e.Prev() {
		item := e.Value.(*CacheItem)
		if time.Now().After(item.LastAccessedBefore.Add(item.Duration)) {
			c.removeElement(e)                      // remove the item from the lru list and itemsByKey map
			c.statsCollector.IncrementExpirations() // increment expirations in stats collector

			// Call the onExpire function if it is not nil
			if item.OnItemExpired != nil {
				item.OnItemExpired(item.Key, item.Value)
			}
		} else {
			break
		}
	}
}

// removeElement removes the given element from the lru list and the itemsByKey map.
func (c *HyperCache) removeElement(e *list.Element) {
	c.lru.Remove(e)
	delete(c.itemsByKey, e.Value.(*CacheItem).Key)
}
