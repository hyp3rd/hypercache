// The Least Recently Used (LRU) eviction algorithm is a page replacement algorithm that discards the least recently used pages first.
// It works by maintaining a queue of pages in memory, with the most recently used page at the front of the queue and the least recently used page at the back.
// When a new page is added to memory and the memory is full, the page at the back of the queue (the least recently used page) is removed to make space for the new page.
// To implement the LRU eviction algorithm, a data structure called a doubly linked list is often used.
// Each page in the list is represented by a node, which contains a pointer to the previous node and a pointer to the next node.
// When a page is accessed, it is moved to the front of the list by updating the pointers of the surrounding nodes.
// This way, the page at the back of the list is always the least recently used page, and can be easily removed when necessary.
// The LRU eviction algorithm is widely used because it performs well in practice, with a low average page fault rate.
// However, it can be somewhat expensive to implement, as it requires updating the data structure every time a page is accessed.
package hypercache

import (
	"errors"
	"sync"
)

type LRUCacheItem struct {
	Key   string
	Value interface{}
	prev  *LRUCacheItem
	next  *LRUCacheItem
}

type LRU struct {
	capacity int
	items    map[string]*LRUCacheItem
	head     *LRUCacheItem
	tail     *LRUCacheItem
	mutex    sync.RWMutex
}

func NewLRU(capacity int) (*LRU, error) {
	if capacity < 0 {
		return nil, ErrInvalidCapacity
	}
	return &LRU{
		capacity: capacity,
		items:    make(map[string]*LRUCacheItem, capacity),
	}, nil
}

func (lru *LRU) Get(key string) (interface{}, bool) {
	lru.mutex.RLock()
	defer lru.mutex.RUnlock()
	item, ok := lru.items[key]
	if !ok {
		return nil, false
	}
	lru.moveToFront(item)
	return item.Value, true
}

func (lru *LRU) Set(key string, value interface{}) {
	lru.mutex.Lock()
	defer lru.mutex.Unlock()
	item, ok := lru.items[key]
	if ok {
		item.Value = value
		lru.moveToFront(item)
		return
	}
	if len(lru.items) == lru.capacity {
		delete(lru.items, lru.tail.Key)
		lru.removeFromList(lru.tail)
	}
	item = &LRUCacheItem{
		Key:   key,
		Value: value,
	}
	lru.items[key] = item
	lru.addToFront(item)
}

func (lru *LRU) Evict() (string, error) {
	lru.mutex.Lock()
	defer lru.mutex.Unlock()
	if lru.tail == nil {
		return "", errors.New("cache is empty")
	}
	key := lru.tail.Key
	lru.removeFromList(lru.tail)
	delete(lru.items, key)
	return key, nil
}

func (lru *LRU) Delete(key string) {
	lru.mutex.Lock()
	defer lru.mutex.Unlock()
	item, ok := lru.items[key]
	if !ok {
		return
	}
	lru.removeFromList(item)
	delete(lru.items, key)
}

func (lru *LRU) moveToFront(item *LRUCacheItem) {
	if item == lru.head {
		return
	}
	lru.removeFromList(item)
	lru.addToFront(item)
}

func (lru *LRU) removeFromList(item *LRUCacheItem) {
	if item == lru.head {
		lru.head = item.next
	} else {
		item.prev.next = item.next
	}
	if item == lru.tail {
		lru.tail = item.prev
	} else {
		item.next.prev = item.prev
	}
	item.prev = nil
	item.next = nil
}

func (lru *LRU) addToFront(item *LRUCacheItem) {
	if lru.head == nil {
		lru.head = item
		lru.tail = item
		return
	}
	item.next = lru.head
	lru.head.prev = item
	lru.head = item
}
