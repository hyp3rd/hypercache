package eviction

// CAWOLFU is an eviction algorithm that uses the Cache-Aware Write-Optimized LFU (CAWOLFU) policy to select items for eviction.

import (
	"sync"

	cache "github.com/hyp3rd/hypercache/cache"
	"github.com/hyp3rd/hypercache/errors"
)

// CAWOLFU is an eviction algorithm that uses the Cache-Aware Write-Optimized LFU (CAWOLFU) policy to select items for eviction.
type CAWOLFU struct {
	items  cache.ConcurrentMap[string, *CAWOLFUNode] // concurrent map to store the items in the cache
	list   *CAWOLFULinkedList                        // linked list to store the items in the cache, with the most frequently used items at the front
	length int                                       // number of items in the cache
	cap    int                                       // capacity of the cache
}

// CAWOLFUNode is a struct that represents a node in the linked list. It has a key, value, and access count field.
type CAWOLFUNode struct {
	key   string       // key of the item
	value any          // value of the item
	count int          // number of times the item has been accessed
	prev  *CAWOLFUNode // previous node in the linked list
	next  *CAWOLFUNode // next node in the linked list
}

// CAWOLFUNodePool is a pool of CAWOLFUNode values.
var CAWOLFUNodePool = sync.Pool{
	New: func() any {
		return &CAWOLFUNode{}
	},
}

// CAWOLFULinkedList is a struct that represents a linked list. It has a head and tail field.
type CAWOLFULinkedList struct {
	head *CAWOLFUNode // head of the linked list
	tail *CAWOLFUNode // tail of the linked list
}

// NewCAWOLFU returns a new CAWOLFU with the given capacity.
func NewCAWOLFU(capacity int) (*CAWOLFU, error) {
	if capacity < 0 {
		return nil, errors.ErrInvalidCapacity
	}

	return &CAWOLFU{
		items: cache.New[*CAWOLFUNode](),
		list:  &CAWOLFULinkedList{},
		cap:   capacity,
	}, nil
}

// Evict returns the next item to be evicted from the cache.
func (c *CAWOLFU) Evict() (string, bool) {
	// if the cache is empty, return an error
	if c.length == 0 {
		return "", false
	}

	// remove the least frequently used item from the cache
	node := c.list.tail
	c.list.remove(node)

	err := c.items.Remove(node.key)
	if err != nil {
		return "", false
	}

	c.length--

	CAWOLFUNodePool.Put(node)

	return node.key, true
}

// Set adds a new item to the cache with the given key.
func (c *CAWOLFU) Set(key string, value any) {
	// if the cache is full, evict an item
	if c.length == c.cap {
		_, _ = c.Evict() // evict an item
	}

	var node *CAWOLFUNode

	node, ok := CAWOLFUNodePool.Get().(*CAWOLFUNode)
	if !ok {
		node = &CAWOLFUNode{}
	}
	// add the new item to the cache
	node.key = key
	node.value = value
	node.count = 1

	c.items.Set(key, node)
	c.addToFront(node)
	c.length++
}

// Get returns the value for the given key from the cache. If the key is not in the cache, it returns false.
func (c *CAWOLFU) Get(key string) (any, bool) {
	node, ok := c.items.Get(key)
	if !ok {
		return nil, false
	}

	node.count++
	c.moveToFront(node)

	return node.value, true
}

// remove removes the given node from the linked list.
// remove removes the given node from the linked list.
func (l *CAWOLFULinkedList) remove(node *CAWOLFUNode) {
	if node == l.head && node == l.tail {
		l.head = nil
		l.tail = nil
	} else if node == l.head {
		l.head = node.next
		l.head.prev = nil
	} else if node == l.tail {
		l.tail = node.prev
		l.tail.next = nil
	} else {
		node.prev.next = node.next
		node.next.prev = node.prev
	}

	node.prev = nil
	node.next = nil
	CAWOLFUNodePool.Put(node)
}

// Delete removes the given key from the cache.
func (c *CAWOLFU) Delete(key string) {
	node, ok := c.items.Get(key)
	if !ok {
		return
	}

	c.list.remove(node)

	err := c.items.Remove(key)
	if err != nil {
		return
	}

	c.length--

	CAWOLFUNodePool.Put(node)
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
	if node == c.list.head {
		return
	}

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
	c.list.head.prev = node
	c.list.head = node
}
