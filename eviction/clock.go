package eviction

// The clock eviction algorithm is a page replacement algorithm that uses a clock-like data structure to keep track of which pages in a computer's memory have been used recently and which have not.
// It works by maintaining a circular buffer of pages, with a "hand" that points to the next page to be replaced.
// When a page needs to be evicted from memory, the hand is advanced to the next page in the buffer, and that page is evicted if it has not been used recently.
// If the page has been used recently, the hand is advanced to the next page, and the process repeats until a page is found that can be evicted.
// The clock eviction algorithm is often used in virtual memory systems to manage the allocation of physical memory.

import (
	"sync"

	"github.com/hyp3rd/hypercache/errors"
	"github.com/hyp3rd/hypercache/types"
)

// ClockAlgorithm is an in-memory cache with the Clock algorithm.
type ClockAlgorithm struct {
	items      []*types.Item
	keys       map[string]int
	mutex      sync.RWMutex
	evictMutex sync.Mutex
	capacity   int
	hand       int
}

// NewClockAlgorithm creates a new in-memory cache with the given capacity and the Clock algorithm.
func NewClockAlgorithm(capacity int) (*ClockAlgorithm, error) {
	if capacity < 0 {
		return nil, errors.ErrInvalidCapacity
	}

	return &ClockAlgorithm{
		items:    make([]*types.Item, capacity),
		keys:     make(map[string]int, capacity),
		capacity: capacity,
		hand:     0,
	}, nil
}

// Evict evicts an item from the cache based on the Clock algorithm.
func (c *ClockAlgorithm) Evict() (string, bool) {
	c.evictMutex.Lock()
	defer c.evictMutex.Unlock()

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
			types.ItemPool.Put(item)

			c.items[c.hand] = nil

			return item.Key, true
		}

		c.hand = (c.hand + 1) % c.capacity
	}

	return "", false
}

// Set sets the item with the given key and value in the cache.
func (c *ClockAlgorithm) Set(key string, value any) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	evictedKey, ok := c.Evict()
	if ok {
		c.Delete(evictedKey)
	}

	var item *types.Item

	item, ok = types.ItemPool.Get().(*types.Item)
	if !ok {
		item = &types.Item{}
	}

	item.Key = key
	item.Value = value
	item.AccessCount = 1

	c.keys[key] = c.hand
	c.items[c.hand] = item
	c.hand = (c.hand + 1) % c.capacity
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
	c.evictMutex.Lock()
	defer c.evictMutex.Unlock()

	index, ok := c.keys[key]
	if !ok {
		return
	}

	item := c.items[index]
	delete(c.keys, key)
	c.items[index] = nil

	types.ItemPool.Put(item)
}
