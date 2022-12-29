// Copyright 2023 F. All rights reserved.
// Use of this source code is governed by a Mozilla Public License 2.0
// license that can be found in the LICENSE file.
// HyperCache is an in-memory cache implementation in Go that supports the expiration and eviction of items.
package v2

import (
	"container/list"
	"fmt"
	"sync"
	"time"
)

// item represents an item in the cache. It stores the key, value, last accessed time, and duration.
type item struct {
	key                string
	value              interface{}
	lastAccessedBefore time.Time
	duration           time.Duration
}

// HyperCache is an in-memory cache that stores items with a key and expiration duration.
// It has a mutex mu to protect concurrent access to the cache,
// a doubly-linked list lru to store the items in the cache, a map itemsByKey to quickly look up items by their key,
// and a capacity field that limits the number of items that can be stored in the cache.
// The stop channel is used to signal the expiration and eviction loops to stop. The evictCh channel is used to signal the eviction loop to start.
type HyperCache struct {
	mu         sync.RWMutex             // mutex to protect concurrent access to the cache
	lru        *list.List               // doubly-linked list to store the items in the cache
	itemsByKey map[string]*list.Element // map to quickly look up items by their key
	capacity   int                      // capacity of the cache, limits the number of items that can be stored in the cache
	stop       chan bool                // channel to signal the expiration and eviction loops to stop
	evictCh    chan bool                // channel to signal the eviction loop to start
	once       sync.Once                // used to ensure that the expiration and eviction loops are only started once
}

// NewHyperCache creates a new in-memory cache with the given capacity.
// If the capacity is negative, it returns an error.
// The function initializes the lru list and itemsByKey map, and starts the expiration and eviction loops in separate goroutines.
func NewHyperCache(capacity int) (cache *HyperCache, err error) {
	if capacity < 0 {
		return nil, fmt.Errorf("invalid capacity: %d", capacity)
	}
	cache = &HyperCache{
		lru:        list.New(),
		itemsByKey: make(map[string]*list.Element),
		capacity:   capacity,
		stop:       make(chan bool, 1),
		evictCh:    make(chan bool, 1),
	}

	// Start expiration and eviction loops if capacity is greater than zero
	if capacity > 0 {
		cache.once.Do(func() {
			tick := time.NewTicker(time.Minute)
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
		})
	}

	return
}

// Set adds a value to the cache with the given key and duration.
// If the key is an empty string, the value is nil, or the duration is negative, it returns an error.
// The function acquires a lock and checks if the item already exists in the cache.
// If it does, it updates the value, duration, and the last accessed time, moves the item to the front of the lru list, and releases the lock.
// If the item doesn't exist, the function checks if the cache is at capacity and evicts the least recently used item if necessary.
// Finally, it creates a new item and adds it to the front of the lru list and the itemsByKey map.
func (c *HyperCache) Set(key string, value interface{}, duration time.Duration) error {
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
	if ee, ok := c.itemsByKey[key]; ok {
		// Update the value, duration, and last accessed time
		ee.Value.(*item).value = value
		ee.Value.(*item).lastAccessedBefore = time.Now()
		ee.Value.(*item).duration = duration
		// Move the item to the front of the lru list
		c.lru.MoveToFront(ee)
		return nil
	}

	var wg sync.WaitGroup
	if c.lru.Len() >= c.capacity {

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
	ee := c.lru.PushFront(&item{key, value, time.Now(), duration})
	c.itemsByKey[key] = ee
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
		item := ee.Value.(*item)
		// Check if the item has expired
		if time.Since(item.lastAccessedBefore) > item.duration {
			return nil, false
		}
		// Update the last accessed time
		item.lastAccessedBefore = time.Now()
		// Move the item to the front of the lru list
		c.lru.MoveToFront(ee)
		return item.value, true
	}
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
func (c *HyperCache) List() []*item {
	c.mu.RLock()
	defer c.mu.RUnlock()

	items := make([]*item, 0, c.lru.Len())

	// Iterate through the lru list and append each item to the slice
	for e := c.lru.Front(); e != nil; e = e.Next() {
		items = append(items, e.Value.(*item))
	}

	return items
}

// Clean removes all items from the cache.
// The function acquires a lock, removes all items from the lru list and itemsByKey map, and releases the lock.
func (c *HyperCache) Clean() {
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

	c.stop <- true
	close(c.stop)
	close(c.evictCh)
}

// Close stops the expiration and eviction loops and cleans the cache.
// The function calls the Stop method to stop the loops and cleans the cache.
func (c *HyperCache) Close() {
	// Stop the expiration and eviction loops
	c.Stop()

	// Clean the cache
	c.Clean()
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
	}

	// Signal that eviction is complete
	c.evictCh <- false
}

// expirationLoop is a goroutine that runs every minute and removes expired items from the cache.
func (c *HyperCache) expirationLoop() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for ee := c.lru.Back(); ee != nil; ee = ee.Prev() {
		i := ee.Value.(*item)
		if i.lastAccessedBefore.Add(i.duration).Before(now) {
			c.removeElement(ee)
		}
	}
}

// removeElement removes the given element from the lru list and the itemsByKey map.
func (c *HyperCache) removeElement(e *list.Element) {
	c.lru.Remove(e)
	delete(c.itemsByKey, e.Value.(*item).key)
}
