package hypercache

import (
	"errors"
	"sync"
)

// ClockCacheItem represents an item in the cache
type ClockCacheItem struct {
	key   string
	Value interface{}
	ref   int
	prev  *ClockCacheItem
	next  *ClockCacheItem
}

// ClockCacheItemPool is a pool of ClockCacheItem values.
var ClockCacheItemPool = sync.Pool{
	New: func() interface{} {
		return &ClockCacheItem{}
	},
}

// Clock represents a clock cache
type ClockCache struct {
	capacity int
	items    map[string]*ClockCacheItem
	head     *ClockCacheItem
	mutex    sync.RWMutex
	cond     *sync.Cond
}

// NewClockCache creates a new clock cache with the given capacity
func NewClockCache(capacity int) (*ClockCache, error) {
	if capacity < 0 {
		return nil, ErrInvalidCapacity
	}
	c := &ClockCache{
		capacity: capacity,
		items:    make(map[string]*ClockCacheItem, capacity),
	}
	c.cond = sync.NewCond(&c.mutex)
	return c, nil
}

// Get retrieves the value for the given key from the cache. If the key is not
// found, it returns false.
func (clock *ClockCache) Get(key string) (interface{}, bool) {
	clock.mutex.RLock()
	defer clock.mutex.RUnlock()
	item, ok := clock.items[key]
	if !ok {
		return nil, false
	}
	item.ref = 1
	return item.Value, true
}

// Set adds the key-value pair to the cache. If the cache is at capacity, it
// evicts the least recently used item.
func (clock *ClockCache) Set(key string, value interface{}) {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	item, ok := clock.items[key]
	if ok {
		item.Value = value
		item.ref = 1
		return
	}
	if len(clock.items) == clock.capacity {
		for len(clock.items) == clock.capacity {
			clock.cond.Wait()
		}
	}
	item = ClockCacheItemPool.Get().(*ClockCacheItem)
	// item = &ClockCacheItem{}
	item.key = key
	item.Value = value
	item.ref = 0

	clock.items[key] = item
	if clock.head == nil {
		clock.head = item
		clock.head.prev = item
		clock.head.next = item
	} else {
		item.prev = clock.head.prev
		item.next = clock.head
		clock.head.prev.next = item
		clock.head.prev = item
		clock.head = item
	}
}

// Evict first acquires a lock on the mutex, then it enters an infinite loop. Inside the loop, it checks if the cache is empty.
// If it is, it returns an error.
// Otherwise, it starts iterating through the linked list and looks for an item that has a reference count of 0.
// If it finds such an item, it removes it from the linked list and the map, releases the lock, and returns the key and a nil error.
// If it doesn't find an item with a reference count of 0, it sets all reference counts to 0 and waits on the condition variable.
// When the function is woken up, it will start the loop again and check for an item with a reference count of 0.
// This process continues until an item with a reference count of 0 is found.
func (clock *ClockCache) Evict() (string, error) {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()

	if clock.head == nil {
		return "", errors.New("cache is empty")
	}

	curr := clock.head
	for {
		if curr.ref == 0 {
			key := curr.key
			clock.removeFromList(curr)
			delete(clock.items, key)
			clock.cond.Signal()
			return key, nil
		}
		curr.ref = 0
		curr = curr.next
		if curr == clock.head {
			break
		}
	}
	return "", errors.New("no item available for eviction")
}

// Delete removes the key-value pair from the cache.
func (clock *ClockCache) Delete(key string) {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	item, ok := clock.items[key]
	if !ok {
		return
	}
	clock.removeFromList(item)
	delete(clock.items, key)
}

// removeFromList removes the item from the linked list and puts it back in the pool.
func (clock *ClockCache) removeFromList(item *ClockCacheItem) {
	if clock.head == item {
		if item.next == item {
			clock.head = nil
		} else {
			clock.head = item.next
			clock.head.prev = item.prev
			item.prev.next = clock.head
		}
	} else {
		item.prev.next = item.next
		item.next.prev = item.prev
	}
	item.prev = nil
	item.next = nil
	ClockCacheItemPool.Put(item)
}
