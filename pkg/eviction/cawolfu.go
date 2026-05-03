package eviction

import (
	"sync"

	"github.com/hyp3rd/hypercache/internal/sentinel"
)

// CAWOLFU is an eviction algorithm that uses the Cache-Aware Write-Optimized LFU (CAWOLFU) policy to select items for eviction.
//
// Concurrency: every method acquires c.mutex for the duration of the
// operation, so the items map needs no internal concurrency machinery —
// a plain map is sufficient. Previously this used pkg/cache.ConcurrentMap
// (v1) whose shard locks were redundant under cawolfu's own mutex.
type CAWOLFU struct {
	mutex    sync.Mutex              // protects all CAWOLFU operations
	items    map[string]*CAWOLFUNode // map to store the items in the cache
	list     *CAWOLFULinkedList      // linked list to store the items in the cache, with the most frequently used items at the front
	length   int                     // number of items in the cache
	cap      int                     // capacity of the cache
	nodePool sync.Pool               // pool of CAWOLFUNode values for memory reuse
}

// CAWOLFUNode is a struct that represents a node in the linked list. It has a key, value, and access count field.
type CAWOLFUNode struct {
	key   string       // key of the item
	value any          // value of the item
	count int          // number of times the item has been accessed
	prev  *CAWOLFUNode // previous node in the linked list
	next  *CAWOLFUNode // next node in the linked list
}

// CAWOLFULinkedList is a struct that represents a linked list. It has a head and tail field.
type CAWOLFULinkedList struct {
	head *CAWOLFUNode // head of the linked list
	tail *CAWOLFUNode // tail of the linked list
}

// NewCAWOLFU returns a new CAWOLFU with the given capacity.
func NewCAWOLFU(capacity int) (*CAWOLFU, error) {
	if capacity < 0 {
		return nil, sentinel.ErrInvalidCapacity
	}

	return &CAWOLFU{
		items: make(map[string]*CAWOLFUNode),
		list:  &CAWOLFULinkedList{},
		cap:   capacity,
		nodePool: sync.Pool{
			New: func() any {
				return &CAWOLFUNode{}
			},
		},
	}, nil
}

// Evict returns the next item to be evicted from the cache.
func (c *CAWOLFU) Evict() (string, bool) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.cap == 0 || c.length == 0 || c.list.tail == nil {
		return "", false
	}

	node := c.list.tail
	c.list.remove(node)

	delete(c.items, node.key)

	c.length--

	// Preserve key before resetting the node for pool reuse
	evictedKey := node.key
	resetCAWOLFUNode(node)
	c.nodePool.Put(node)

	return evictedKey, true
}

// Set adds a new item to the cache with the given key.
func (c *CAWOLFU) Set(key string, value any) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.cap == 0 {
		// Zero-capacity CAWOLFU is a no-op
		return
	}

	// If key exists, update value and count, move to front
	if node, ok := c.items[key]; ok {
		node.value = value
		node.count++
		c.moveToFront(node)

		return
	}

	// Inline eviction logic to avoid deadlock
	if c.length == c.cap { // eviction path
		if c.list.tail == nil { // nothing to evict
			return
		}

		evicted := c.list.tail
		c.list.remove(evicted)

		delete(c.items, evicted.key)

		c.length--

		evictedKey := evicted.key
		resetCAWOLFUNode(evicted)
		c.nodePool.Put(evicted)

		if evictedKey == key { // same key evicted, abort insert
			return
		}
	}

	node, ok := c.nodePool.Get().(*CAWOLFUNode)
	if !ok {
		node = &CAWOLFUNode{}
	}

	node.key = key
	node.value = value
	node.count = 1
	c.items[key] = node
	c.addToFront(node)

	c.length++
}

// Get returns the value for the given key from the cache. If the key is not in the cache, it returns false.
func (c *CAWOLFU) Get(key string) (any, bool) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	node, ok := c.items[key]
	if !ok {
		return nil, false
	}

	node.count++
	c.moveToFront(node)

	return node.value, true
}

// remove removes the given node from the linked list.
func (l *CAWOLFULinkedList) remove(node *CAWOLFUNode) {
	switch {
	case l.head == l.tail: // only one element in the list
		l.head = nil
		l.tail = nil

	case node == l.head:
		l.head = node.next
		l.head.prev = nil

	case node == l.tail:
		l.tail = node.prev
		l.tail.next = nil

	default:
		node.prev.next = node.next
		node.next.prev = node.prev
	}

	node.prev = nil
	node.next = nil
}

// Delete removes the given key from the cache.
func (c *CAWOLFU) Delete(key string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	node, ok := c.items[key]
	if !ok {
		return
	}

	c.list.remove(node)
	delete(c.items, key)

	c.length--

	resetCAWOLFUNode(node)
	c.nodePool.Put(node)
}

// resetCAWOLFUNode clears all fields of a CAWOLFUNode before returning to pool.
func resetCAWOLFUNode(node *CAWOLFUNode) {
	node.key = ""
	node.value = nil
	node.count = 0
	node.prev = nil
	node.next = nil
}

// addToFront adds the given node to the front of the linked list.
func (c *CAWOLFU) addToFront(node *CAWOLFUNode) {
	node.prev = nil

	node.next = c.list.head
	if c.list.head != nil {
		c.list.head.prev = node
	}

	c.list.head = node
	if c.list.tail == nil {
		c.list.tail = node
	}
}

// moveToFront moves the given node to the front of the linked list.
func (c *CAWOLFU) moveToFront(node *CAWOLFUNode) {
	if node == nil || node == c.list.head {
		return
	}

	// Remove node from its current position
	if node == c.list.tail {
		c.list.tail = node.prev
	}

	if node.prev != nil {
		node.prev.next = node.next
	}

	if node.next != nil {
		node.next.prev = node.prev
	}

	node.prev = nil

	node.next = c.list.head
	if c.list.head != nil {
		c.list.head.prev = node
	}

	c.list.head = node
	if c.list.tail == nil {
		c.list.tail = node
	}
}
