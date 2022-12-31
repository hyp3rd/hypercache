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
	maxEvictionCount   uint                     // maximum number of items that can be evicted in a single eviction loop iteration
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

// SetOptions applies the given options to the cache.
func (c *HyperCache) SetOptions(options ...Option) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, option := range options {
		option(c)
	}
}

// SetCapacity sets the capacity of the cache. If the new capacity is less than the current cache size, it evicts items to bring the cache size down to the new capacity.
func (c *HyperCache) SetCapacity(capacity int) error {
	// If the new capacity is negative, return an error
	if capacity < 0 {
		return fmt.Errorf("invalid capacity: %d", capacity)
	}

	// Acquire a lock to protect concurrent access to the cache
	c.mu.Lock()
	// Set the new capacity
	c.capacity = capacity
	// Release the lock
	c.mu.Unlock()

	// Evict the least recently used item
	if c.Len() > capacity {
		c.evictCh <- true
		<-c.evictCh
		// c.Evict(c.Len() - capacity)
	}

	return nil
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
func (c *HyperCache) Add(cacheItem *CacheItem) error {
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

	// Acquire a lock to protect concurrent access to the cache
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

	// Evict the least recently used item
	if c.lru.Len() >= c.capacity {

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.evictCh <- true
			<-c.evictCh
		}()

		go func() {
			// Wait for eviction to complete
			wg.Wait()

		}()
	}

	// Create a new item and add it to the front of the lru list and itemsByKey map
	newItem := &CacheItem{
		Key:                cacheItem.Key,
		Value:              cacheItem.Value,
		LastAccessedBefore: time.Now(),
		Duration:           cacheItem.Duration,
		OnItemExpired:      cacheItem.OnItemExpired,
	}
	c.itemsByKey[cacheItem.Key] = c.lru.PushFront(newItem)

	go c.statsCollector.IncrementMisses() // increment misses in stats collector
	return nil
}

// Set adds a value to the cache with the given key and duration.
// If the key is an empty string, the value is nil, or the duration is negative, it returns an error.
// If it does, it updates the value, duration, and the last accessed time, moves the item to the front of the lru list, and releases the lock.
// If the item doesn't exist, the function checks if the cache is at capacity and evicts the least recently used item if necessary.
func (c *HyperCache) Set(key string, value interface{}, duration time.Duration) error {
	// Check for invalid key, value, or duration
	if key == "" {
		return fmt.Errorf("key cannot be empty")
	}
	if value == nil {
		return fmt.Errorf("value cannot be nil")
	}

	cacheItem := &CacheItem{
		Key:      key,
		Value:    value,
		Duration: duration,
	}

	return c.Add(cacheItem)
}

// SetWithOptions sets an item in the cache with the given key and value.
// If the key already exists in the cache, it updates the value and moves the item to the front of the lru list.
// If the key doesn't exist in the cache, it creates a new cache item and adds it to the front of the lru list.
func (c *HyperCache) SetWithOptions(key string, value interface{}, options ...CacheItemOption) (err error) {
	// Check for invalid key, value, or duration
	if key == "" {
		return fmt.Errorf("key cannot be empty")
	}
	if value == nil {
		return fmt.Errorf("value cannot be nil")
	}

	// Key doesn't exist in the cache, create a new cache item and add it to the front of the lru list
	cacheItem := &CacheItem{
		Key:                key,
		Value:              value,
		LastAccessedBefore: time.Now(),
	}

	// Set the options for the new cache item
	for _, option := range options {
		option(cacheItem)
	}

	return c.Add(cacheItem)
}

// Get retrieves the value associated with the given key from the cache.
// If the item is not found in the cache, it returns an error.
// The function updates the item's last accessed time before returning it.
func (c *HyperCache) Get(key string) (value interface{}, ok bool) {
	// Lock the cache's mutex to ensure thread-safety
	c.mu.Lock()
	defer c.mu.Unlock()

	if element, ok := c.itemsByKey[key]; ok {
		// Retrieve the cache item and update its last accessed time
		item := element.Value.(*CacheItem)
		item.Touch()
		c.lru.MoveToFront(element)

		go c.statsCollector.IncrementHits() // increment hits in stats collector
		return item.Value, true
	}

	go c.statsCollector.IncrementMisses() // increment misses in stats collector
	// Return an error if the item is not found in the cache
	return nil, false
}

// GetItem gets an item from the cache.
// If the key exists in the cache, it returns the item and true.
// If the key doesn't exist in the cache, it returns nil and false.
func (c *HyperCache) GetItem(key string) (item *CacheItem, ok bool) {
	if key == "" {
		return nil, false
	}

	// Acquire a lock to protect concurrent access to the cache
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
		return item, true
	}

	go c.statsCollector.IncrementMisses() // increment misses in stats collector
	return nil, false
}

// GetOrSet retrieves the value of a cache item with the given key, or sets a new value in the cache if the key doesn't exist.
// It returns the value and a boolean indicating whether the value was retrieved from the cache (true) or set in the cache (false).
func (c *HyperCache) GetOrSet(key string, value interface{}, options ...CacheItemOption) (interface{}, bool) {
	// Try to get the value from the cache
	item, ok := c.GetItem(key)
	if ok {
		// Value was retrieved from the cache, return it
		return item.Value, true
	}

	// Value was not found in the cache, set it in the cache
	c.SetWithOptions(key, value, options...)
	return value, false
}

// GetMultiple retrieves the values associated with the given keys from the cache.
// It returns a map of keys to values for the items found in the cache,
// and an error if any of the items are not found in the cache.
func (c *HyperCache) GetMultiple(keys ...string) (values map[string]interface{}, err error) {
	values = make(map[string]interface{})
	for _, key := range keys {
		// Retrieve each item from the cache individually
		value, ok := c.Get(key)
		if !ok {
			return nil, err
		}
		if ok {
			values[key] = value
		}
	}
	return values, nil
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
	// Acquire a read lock to protect concurrent access to the cache
	c.mu.RLock()
	// Get the capacity of the cache
	capacity := c.capacity
	// Release the read lock
	c.mu.RUnlock()

	return capacity
}

// Stats returns the statistics collected by the stats collector.
func (c *HyperCache) Stats() any {
	return c.statsCollector.GetStats()
}

// The Stop function stops the expiration and eviction loops and closes the stop channel.
// The function acquires a lock and sends a value to the stop and evictCh channels.
func (c *HyperCache) Stop() {
	// Acquire a lock to protect concurrent access to the cache
	c.mu.Lock()

	// Stop the expiration and eviction loops
	c.once = sync.Once{}
	c.stop <- true
	close(c.stop)
	close(c.evictCh)

	// Release the lock
	c.mu.Unlock()
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

// evict evicts items from the cache. It removes the specified number of items from the end of the lru list,
// up to the number of items needed to bring the cache size down to the capacity.
func (c *HyperCache) Evict(count int) {
	// Find the number of items to evict
	toEvict := c.lru.Len() - c.capacity
	if count > 0 && count < toEvict {
		// If a count is specified and it is less than the number of items needed to bring the cache size down to the capacity,
		// only evict the specified number of items
		toEvict = count
	}

	// Evict the items
	// Continue removing the least recently used items until the cache is below capacity
	for i := 0; i <= toEvict; i++ {
		// Get the last item in the lru list
		item := c.lru.Back()
		if item == nil {
			// lru list is empty, break out of the loop
			break
		}
		// Remove the item from the cache
		c.removeElement(item)
		go c.statsCollector.IncrementEvictions() // increment evictions in stats collector
	}
}

// The evictionLoop function removes the least recently used items from the cache until it is below the capacity.
// The function acquires a lock and iterates over the items in the lru list, starting from the back.
// It removes the item from the lru list and the itemsByKey map if the cache is at capacity.
// evictionLoop evicts the least recently used items from the cache until the size of the cache is below the capacity.
func (c *HyperCache) evictionLoop() {
	c.mu.Lock()
	// defer c.mu.Unlock()

	// Evict the least recently used items from the cache until the size of the cache is below the capacity
	c.Evict(int(c.maxEvictionCount))

	// Signal that eviction is complete
	c.evictCh <- true
	c.mu.Unlock()
}

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
