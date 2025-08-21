// Package eviction - The clock eviction algorithm is a page replacement algorithm that uses a clock-like data structure to keep track of which pages in a computer's memory have been used recently and which have not.
// It works by maintaining a circular buffer of pages, with a "hand" that points to the next page to be replaced.
// When a page needs to be evicted from memory, the hand is advanced to the next page in the buffer, and that page is evicted if it has not been used recently.
// If the page has been used recently, the hand is advanced to the next page, and the process repeats until a page is found that can be evicted.
// The clock eviction algorithm is often used in virtual memory systems to manage the allocation of physical memory.
package eviction

import (
	"sync"

	"github.com/hyp3rd/hypercache/internal/sentinel"
	"github.com/hyp3rd/hypercache/pkg/cache"
)

// ClockAlgorithm is an in-memory cache with the Clock algorithm.
type ClockAlgorithm struct {
	items           []*cache.Item
	itemPoolManager *cache.ItemPoolManager
	keys            map[string]int
	mutex           sync.RWMutex
	capacity        int
	hand            int
}

// NewClockAlgorithm creates a new in-memory cache with the given capacity and the Clock algorithm.
func NewClockAlgorithm(capacity int) (*ClockAlgorithm, error) {
	if capacity < 0 {
		return nil, sentinel.ErrInvalidCapacity
	}

	return &ClockAlgorithm{
		items:           make([]*cache.Item, capacity),
		itemPoolManager: cache.NewItemPoolManager(),
		keys:            make(map[string]int, capacity),
		capacity:        capacity,
		hand:            0,
	}, nil
}

// Evict evicts an item from the cache based on the Clock algorithm.
func (c *ClockAlgorithm) Evict() (string, bool) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.capacity == 0 {
		return "", false
	}

	for range c.capacity {
		item := c.items[c.hand]
		if item == nil {
			c.hand = (c.hand + 1) % c.capacity

			continue
		}

		if item.AccessCount > 0 {
			item.AccessCount--
		} else {
			// Preserve key before zeroing the item back to the pool
			evictedKey := item.Key
			delete(c.keys, evictedKey)
			c.itemPoolManager.Put(item)
			c.items[c.hand] = nil

			return evictedKey, true
		}

		c.hand = (c.hand + 1) % c.capacity
	}

	return "", false
}

// Set sets the item with the given key and value in the cache.
func (c *ClockAlgorithm) Set(key string, value any) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.capacity == 0 {
		// Zero-capacity Clock is a no-op
		return
	}

	// If key exists, update value and access count
	if idx, ok := c.keys[key]; ok {
		item := c.items[idx]
		item.Value = value
		item.AccessCount++

		return
	}

	// Find next available slot or evict if full (inline logic)
	start := c.hand
	inserted := false

	for {
		if c.items[c.hand] == nil {
			item := c.itemPoolManager.Get()
			item.Key = key
			item.Value = value
			item.AccessCount = 1
			c.items[c.hand] = item
			c.keys[key] = c.hand
			inserted = true

			break
		}

		c.hand = (c.hand + 1) % c.capacity
		if c.hand == start {
			break // full
		}
	}

	if !inserted {
		// All slots full, evict one (inline)
		for range c.capacity {
			item := c.items[c.hand]
			if item == nil {
				c.hand = (c.hand + 1) % c.capacity

				continue
			}

			if item.AccessCount > 0 {
				item.AccessCount--
			} else {
				delete(c.keys, item.Key)
				c.itemPoolManager.Put(item)
				c.items[c.hand] = nil
				// After eviction, insert at current hand
				newItem := c.itemPoolManager.Get()
				newItem.Key = key
				newItem.Value = value
				newItem.AccessCount = 1
				c.items[c.hand] = newItem
				c.keys[key] = c.hand
				c.hand = (c.hand + 1) % c.capacity

				return
			}

			c.hand = (c.hand + 1) % c.capacity
		}
	} else {
		c.hand = (c.hand + 1) % c.capacity
	}
}

// Get retrieves the item with the given key from the cache.
func (c *ClockAlgorithm) Get(key string) (any, bool) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	index, ok := c.keys[key]
	if !ok {
		return nil, false
	}

	item := c.items[index]
	item.AccessCount++

	return item.Value, true
}

// Delete deletes the item with the given key from the cache.
func (c *ClockAlgorithm) Delete(key string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	index, ok := c.keys[key]
	if !ok {
		return
	}

	item := c.items[index]
	delete(c.keys, key)
	c.items[index] = nil
	c.itemPoolManager.Put(item)
}
