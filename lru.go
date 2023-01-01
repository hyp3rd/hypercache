package hypercache

import (
	"container/list"
	"sync"
)

type LRU struct {
	// Use a doubly-linked list and a hash map to implement the LRU cache
	list  *list.List
	items map[string]*list.Element
	// Use a mutex to synchronize access to the data structures
	mu sync.Mutex
	// Use a capacity field to limit the size of the cache
	capacity int
}

// LRUItem represents an item in the LRU cache
type LRUItem struct {
	key   string
	value interface{}
}

// NewLRU creates and returns a new LRU cache with the specified capacity
func NewLRU(capacity int) *LRU {
	return &LRU{
		list:     list.New(),
		items:    make(map[string]*list.Element),
		capacity: capacity,
	}
}

// Add adds an item to the LRU cache
func (l *LRU) Add(key string, value interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Check if the item is already in the cache
	if element, ok := l.items[key]; ok {
		// Update the value of the item and move it to the front of the linked list
		item := element.Value.(*LRUItem)
		item.value = value
		l.list.MoveToFront(element)
		return
	}

	// Create a new LRUItem for the value
	item := &LRUItem{
		key:   key,
		value: value,
	}
	// Add the item to the front of the linked list and to the hash map
	element := l.list.PushFront(item)
	l.items[key] = element
	// Evict the least-recently-used item if the cache is full
	if l.list.Len() > l.capacity {
		l.removeOldest()
	}
}

// Remove removes an item from the LRU cache
func (l *LRU) Remove(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Check if the item is in the cache
	if element, ok := l.items[key]; ok {
		// Remove the item from the linked list and the hash map
		l.list.Remove(element)
		delete(l.items, key)
	}
}

// Get removes and returns the least-recently-used item from the LRU cache
func (l *LRU) Get() (key string, value interface{}, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Get the last element in the linked list (the least-recently-used item)
	element := l.list.Back()
	if element == nil {
		// If the linked list is empty, return false
		return "", nil, false
	}
	// Remove the item from the linked list and the hash map
	item := l.list.Remove(element).(*LRUItem)
	delete(l.items, item.key)
	// Return the key and value of the item
	return item.key, item.value, true
}

// removeOldest removes the least-recently-used item from the LRU cache
func (l *LRU) removeOldest() {
	// Get the last element in the linked list (the least-recently-used item)
	element := l.list.Back()
	if element == nil {
		return
	}
	// Remove the item from the linked list and the hash map
	item := l.list.Remove(element).(*LRUItem)
	delete(l.items, item.key)
}

// Len returns the number of items in the LRU cache
func (l *LRU) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.list.Len()
}

// Clear removes all items from the LRU cache
func (l *LRU) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.list.Init()
	l.items = make(map[string]*list.Element)
}
