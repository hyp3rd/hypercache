package eviction

import (
	"sync"

	"github.com/hyp3rd/hypercache/internal/sentinel"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
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
		if item == nil { // empty slot, advance
			c.hand = (c.hand + 1) % c.capacity

			continue
		}

		if item.AccessCount > 0 { // give second chance
			item.AccessCount--

			c.hand = (c.hand + 1) % c.capacity

			continue
		}

		// evict
		evictedKey := item.Key
		delete(c.keys, evictedKey)
		c.itemPoolManager.Put(item)

		c.items[c.hand] = nil

		return evictedKey, true
	}

	return "", false
}

// Set sets the item with the given key and value in the cache.
func (c *ClockAlgorithm) Set(key string, value any) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.capacity == 0 { // zero-capacity is no-op
		return
	}

	if c.updateIfExists(key, value) { // fast path
		return
	}

	if c.tryInsertInFreeSlot(key, value) { // inserted
		c.advanceHand()

		return
	}

	c.evictAndInsert(key, value)
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

// -- helpers (unexported) --

func (c *ClockAlgorithm) updateIfExists(key string, value any) bool { //nolint:ireturn
	if idx, ok := c.keys[key]; ok {
		item := c.items[idx]

		item.Value = value
		item.AccessCount++

		return true
	}

	return false
}

func (c *ClockAlgorithm) tryInsertInFreeSlot(key string, value any) bool { //nolint:ireturn
	start := c.hand
	for {
		if c.items[c.hand] == nil { // free slot
			item := c.itemPoolManager.Get()

			item.Key = key
			item.Value = value
			item.AccessCount = 1
			c.items[c.hand] = item
			c.keys[key] = c.hand

			return true
		}

		c.advanceHand()

		if c.hand == start { // wrapped -> full
			return false
		}
	}
}

func (c *ClockAlgorithm) evictAndInsert(key string, value any) { //nolint:ireturn
	for range c.capacity {
		item := c.items[c.hand]
		if item == nil { // skip empty
			c.advanceHand()

			continue
		}

		if item.AccessCount > 0 { // second chance
			item.AccessCount--

			c.advanceHand()

			continue
		}

		delete(c.keys, item.Key)
		c.itemPoolManager.Put(item)

		c.items[c.hand] = nil

		newItem := c.itemPoolManager.Get()

		newItem.Key = key
		newItem.Value = value
		newItem.AccessCount = 1
		c.items[c.hand] = newItem
		c.keys[key] = c.hand
		c.advanceHand()

		return
	}
}

func (c *ClockAlgorithm) advanceHand() { // small helper
	c.hand = (c.hand + 1) % c.capacity
}
