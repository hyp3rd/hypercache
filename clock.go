package hypercache

// The clock eviction algorithm is a page replacement algorithm that uses a clock-like data structure to keep track of which pages in a computer's memory have been used recently and which have not.
// It works by maintaining a circular buffer of pages, with a "hand" that points to the next page to be replaced.
// When a page needs to be evicted from memory, the hand is advanced to the next page in the buffer, and that page is evicted if it has not been used recently.
// If the page has been used recently, the hand is advanced to the next page, and the process repeats until a page is found that can be evicted.
// The clock eviction algorithm is often used in virtual memory systems to manage the allocation of physical memory.

import (
	"sync"
	"time"

	"github.com/hyp3rd/hypercache/cache"
	"github.com/hyp3rd/hypercache/errors"
)

// ClockAlgorithm is an in-memory cache with the Clock algorithm.
type ClockAlgorithm struct {
	items    map[string]*cache.CacheItem
	mutex    sync.RWMutex
	capacity int
}

// NewClockAlgorithm creates a new in-memory cache with the given capacity and the Clock algorithm.
func NewClockAlgorithm(capacity int) (*ClockAlgorithm, error) {
	if capacity < 0 {
		return nil, errors.ErrInvalidCapacity
	}

	return &ClockAlgorithm{
		items:    make(map[string]*cache.CacheItem, capacity),
		capacity: capacity,
	}, nil
}

// Evict evicts the least recently used item from the cache.
func (c *ClockAlgorithm) Evict() (string, bool) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	var oldestKey string
	oldestTime := time.Now()
	for key, item := range c.items {
		if item.LastAccess.Before(oldestTime) {
			oldestTime = item.LastAccess
			oldestKey = key
		}
	}
	if oldestKey == "" {
		return "", false
	}
	c.Delete(oldestKey)
	return oldestKey, true
}

// Set sets the item with the given key and value in the cache.
func (c *ClockAlgorithm) Set(key string, value any) {
	// c.mutex.RLock()
	// defer c.mutex.RUnlock()

	item := cache.CacheItemPool.Get().(*cache.CacheItem)
	item.Value = value
	item.LastAccess = time.Now()
	item.AccessCount = 0
	c.items[key] = item
}

// Get retrieves the item with the given key from the cache.
func (c *ClockAlgorithm) Get(key string) (any, bool) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	item, ok := c.items[key]
	if !ok {
		return nil, false
	}
	item.LastAccess = time.Now()
	item.AccessCount++
	return item.Value, true
}

// Delete deletes the item with the given key from the cache.
func (c *ClockAlgorithm) Delete(key string) {
	// c.mutex.RLock()
	// defer c.mutex.RUnlock()

	item, ok := c.items[key]
	if !ok {
		return
	}
	delete(c.items, key)
	cache.CacheItemPool.Put(item)
}
