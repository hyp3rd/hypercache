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
// The stop channel is used to signal the expiration and eviction loops to stop.
type HyperCache struct {
	mu         sync.RWMutex
	lru        *list.List
	itemsByKey map[string]*list.Element
	capacity   int
	stop       chan bool
	evictCh    chan bool
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
		stop:       make(chan bool),
		evictCh:    make(chan bool, 1),
	}

	// Start expiration and eviction loops if capacity is greater than zero
	if capacity > 0 {
		tick := time.NewTicker(time.Minute)
		go func() {
			for {
				select {
				case <-tick.C:
					cache.expirationLoop()
					cache.evictionLoop()
				case <-cache.stop:
					return
				}
			}
		}()
	}

	return
}

// The Set function adds a value to the cache with the given key and duration.
// If the key is an empty string, the value is nil, or the duration is negative, it returns an error.
// The function acquires a lock and checks if the item already exists in the cache.
// If it does, it updates the value and the last accessed time, moves the item to the front of the lru list, and releases the lock.
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
		// Update the value and the last accessed time
		item := ee.Value.(*item)
		item.value = value
		item.lastAccessedBefore = time.Now()
		item.duration = duration
		// Move the item to the front of the lru list
		c.lru.MoveToFront(ee)
		return nil
	}

	// Check if the cache is at capacity
	if c.lru.Len() == c.capacity {
		// Evict the least recently used item
		ee := c.lru.Back()
		c.lru.Remove(ee)
		item := ee.Value.(*item)
		delete(c.itemsByKey, item.key)
	}

	// Create a new item
	item := &item{
		key:                key,
		value:              value,
		lastAccessedBefore: time.Now(),
		duration:           duration,
	}
	ee := c.lru.PushFront(item)
	c.itemsByKey[key] = ee

	return nil
}

// The Get function returns the value for the given key from the cache.
// If the key is not found or has expired, it returns nil.
// The function acquires a lock and checks if the item exists in the cache and has not expired.
// If it does, it updates the last accessed time, moves the item to the front of the lru list, and releases the lock.
func (c *HyperCache) Get(key string) (value interface{}, ok bool) {
	if key == "" {
		return nil, false
	}

	c.mu.RLock()
	// Check if the item exists
	if ee, ok := c.itemsByKey[key]; ok {
		item := ee.Value.(*item)
		// Check if the item has expired
		if time.Since(item.lastAccessedBefore) > item.duration {
			c.mu.RUnlock()
			return nil, false
		}
		// Update the last accessed time
		item.lastAccessedBefore = time.Now()
		// Move the item to the front of the lru list
		c.lru.MoveToFront(ee)
		c.mu.RUnlock()
		return item.value, true
	}
	c.mu.RUnlock()
	return nil, false
}

// The Delete function removes the item with the given key from the cache.
// If the key is not found, it returns nil.
// The function acquires a lock and removes the item from the lru list and the itemsByKey map.
func (c *HyperCache) Delete(key string) error {
	if key == "" {
		return fmt.Errorf("key cannot be empty")
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	// Check if the item exists
	if ee, ok := c.itemsByKey[key]; ok {
		delete(c.itemsByKey, key)
		c.lru.Remove(ee)
		return nil
	}
	return fmt.Errorf("key not found in cache")
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
// It acquires a lock, clears the lru list and the itemsByKey map, and releases the lock.
func (c *HyperCache) Clean() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.lru.Init()
	c.itemsByKey = make(map[string]*list.Element)
}

// The Len function returns the number of items in the cache.
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
// The function acquires a lock and sends a value to the stop channel.
func (c *HyperCache) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.stop <- true
	close(c.stop)
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
func (c *HyperCache) evictionLoop() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for ee := c.lru.Back(); ee != nil; ee = c.lru.Back() {
		// Check if the cache is at capacity
		if c.lru.Len() <= c.capacity {
			break
		}
		c.lru.Remove(ee)
		item := ee.Value.(*item)
		delete(c.itemsByKey, item.key)
	}
}

// The expirationLoop function removes expired items from the cache.
// The function acquires a lock and iterates over the items in the lru list, starting from the back.
// It removes the item from the lru list and the itemsByKey map if it has expired.
func (c *HyperCache) expirationLoop() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for ee := c.lru.Back(); ee != nil; ee = ee.Prev() {
		item := ee.Value.(*item)
		// Check if the item has expired
		if time.Since(item.lastAccessedBefore) > item.duration {
			c.lru.Remove(ee)
			delete(c.itemsByKey, item.key)
		}
	}
}
