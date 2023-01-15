package eviction

// ARC is an in-memory cache that uses the Adaptive Replacement Cache (ARC) algorithm to manage its items.
// It has a map of items to store the items in the cache, and a capacity field that limits the number of items that can be stored in the cache.
// The ARC algorithm uses two lists, t1 and t2, to store the items in the cache.
// The p field represents the "promotion threshold", which determines how many items should be stored in t1.
// The c field represents the current number of items in the cache.

import (
	"sync"

	"github.com/hyp3rd/hypercache/errors"
	"github.com/hyp3rd/hypercache/models"
)

// ARC is an in-memory cache that uses the Adaptive Replacement Cache (ARC) algorithm to manage its items.
type ARC struct {
	capacity int                     // capacity is the maximum number of items that can be stored in the cache
	t1       map[string]*models.Item // t1 is a list of items that have been accessed recently
	t2       map[string]*models.Item // t2 is a list of items that have been accessed less recently
	b1       map[string]bool         // b1 is a list of items that have been evicted from t1
	b2       map[string]bool         // b2 is a list of items that have been evicted from t2
	p        int                     // p is the promotion threshold
	c        int                     // c is the current number of items in the cache
	mutex    sync.RWMutex            // mutex is a read-write mutex that protects the cache
}

// NewARC creates a new in-memory cache with the given capacity and the Adaptive Replacement Cache (ARC) algorithm.
// If the capacity is negative, it returns an error.
func NewARC(capacity int) (*ARC, error) {
	if capacity < 0 {
		return nil, errors.ErrInvalidCapacity
	}
	return &ARC{
		capacity: capacity,
		t1:       make(map[string]*models.Item, capacity),
		t2:       make(map[string]*models.Item, capacity),
		b1:       make(map[string]bool, capacity),
		b2:       make(map[string]bool, capacity),
		p:        0,
		c:        0,
	}, nil
}

// Get retrieves the item with the given key from the cache.
// If the key is not found in the cache, it returns nil.
func (arc *ARC) Get(key string) (any, bool) {
	arc.mutex.RLock()
	defer arc.mutex.RUnlock()

	// Check t1
	item, ok := arc.t1[key]
	if ok {
		arc.promote(key)
		return item.Value, true
	}
	// Check t2
	item, ok = arc.t2[key]
	if ok {
		arc.demote(key)
		return item.Value, true
	}
	// arc.mutex.RUnlock()
	return nil, false
}

// Promote moves the item with the given key from t2 to t1.
func (arc *ARC) promote(key string) {
	item, ok := arc.t2[key]
	if !ok {
		return
	}
	delete(arc.t2, key)
	arc.t1[key] = item
	arc.p++
	if arc.p > arc.capacity {
		arc.p = arc.capacity
	}
}

// Demote moves the item with the given key from t1 to t2.
func (arc *ARC) demote(key string) {
	item, ok := arc.t1[key]
	if !ok {
		return
	}
	delete(arc.t1, key)
	arc.t2[key] = item
	arc.p--
	if arc.p < 0 {
		arc.p = 0
	}
}

// Set adds a new item to the cache with the given key.
func (arc *ARC) Set(key string, value any) {
	arc.mutex.RLock()
	defer arc.mutex.RUnlock()
	// Check if key is already in cache
	_, ok := arc.Get(key)
	if ok {
		return
	}

	// Check if cache is at capacity
	if arc.c >= arc.capacity {
		// Eviction needed
		evictedKey, ok := arc.Evict()
		if !ok {
			return
		}
		arc.Delete(evictedKey)
	}
	// Add new item to cache
	item := models.ItemPool.Get().(*models.Item)
	item.Value = value

	arc.t1[key] = item
	arc.c++
	arc.p++
	if arc.p > arc.capacity {
		arc.p = arc.capacity
	}
}

// Delete removes the item with the given key from the cache.
func (arc *ARC) Delete(key string) {
	// Check t1
	item, ok := arc.t1[key]
	if ok {
		delete(arc.t1, key)
		arc.c--
		arc.p--
		if arc.p < 0 {
			arc.p = 0
		}
		models.ItemPool.Put(item)
		return
	}
	// Check t2
	item, ok = arc.t2[key]
	if ok {
		delete(arc.t2, key)
		arc.c--
		models.ItemPool.Put(item)
	}
}

// Evict removes an item from the cache and returns the key of the evicted item.
// If no item can be evicted, it returns an error.
func (arc *ARC) Evict() (string, bool) {
	// Check t1
	for key, val := range arc.t1 {
		delete(arc.t1, key)
		arc.c--
		models.ItemPool.Put(val)
		return key, true
	}
	// Check t2
	for key, val := range arc.t2 {
		delete(arc.t2, key)
		arc.c--
		models.ItemPool.Put(val)
		return key, true
	}
	return "", false
}
