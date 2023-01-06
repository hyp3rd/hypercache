package hypercache

// Least Frequently Used (LFU) eviction algorithm implementation

import (
	"sync"
)

// LFUAlgorithm is an eviction algorithm that uses the Least Frequently Used (LFU) policy to select items for eviction.
type LFUAlgorithm struct {
	items  map[string]*Node // map to store the items in the cache
	list   *LinkedList      // linked list to store the items in the cache, with the most frequently used items at the front
	mutex  sync.RWMutex     // mutex to protect the linked list
	length int              // number of items in the cache
	cap    int              // capacity of the cache
}

// Node is a struct that represents a node in the linked list. It has a key, value, and access count field.
type Node struct {
	key   string      // key of the item
	value interface{} // value of the item
	count int         // number of times the item has been accessed
	prev  *Node       // previous node in the linked list
	next  *Node       // next node in the linked list
}

// LFUNodePool is a pool of Node values.
var LFUNodePool = sync.Pool{
	New: func() interface{} {
		return &Node{}
	},
}

// LinkedList is a struct that represents a linked list. It has a head and tail field.
type LinkedList struct {
	head *Node // head of the linked list
	tail *Node // tail of the linked list
}

// NewLFUAlgorithm returns a new LFUAlgorithm with the given capacity.
func NewLFUAlgorithm(capacity int) (*LFUAlgorithm, error) {
	if capacity < 0 {
		return nil, ErrInvalidCapacity
	}

	return &LFUAlgorithm{
		items:  make(map[string]*Node, capacity),
		list:   &LinkedList{},
		length: 0,
		cap:    capacity,
	}, nil
}

// Evict returns the next item to be evicted from the cache.
func (l *LFUAlgorithm) Evict() (string, bool) {
	l.mutex.RLock()
	defer l.mutex.RUnlock()
	// if the cache is empty, return an error
	if l.length == 0 {
		return "", false
	}

	// remove the least frequently used item from the cache
	node := l.list.tail
	l.remove(node)
	delete(l.items, node.key)
	l.length--

	return node.key, true
}

// Set adds a new item to the cache with the given key.
func (l *LFUAlgorithm) Set(key string, value interface{}) {
	l.mutex.Lock()
	// if the cache is full, evict an item
	if l.length == l.cap {
		l.mutex.Unlock() // unlock before evicting to avoid deadlock
		_, _ = l.Evict() // evict an item
		l.mutex.Lock()   // lock again
	}

	// add the new item to the cache
	node := LFUNodePool.Get().(*Node)
	node.count = 1
	node.key = key
	node.value = value

	l.items[key] = node
	l.addToFront(node)
	l.length++
	l.mutex.Unlock()
}

// Get returns the value for the given key from the cache. If the key is not in the cache, it returns false.
func (l *LFUAlgorithm) Get(key string) (interface{}, bool) {
	l.mutex.RLock()
	defer l.mutex.RUnlock()
	node, ok := l.items[key]
	if !ok {
		return nil, false
	}
	node.count++
	l.moveToFront(node)
	return node.value, true
}

// remove removes the given node from the linked list.
func (l *LFUAlgorithm) remove(node *Node) {
	if node == l.list.head && node == l.list.tail {
		l.list.head = nil
		l.list.tail = nil
	} else if node == l.list.head {
		l.list.head = node.next
		node.next.prev = nil
	} else if node == l.list.tail {
		l.list.tail = node.prev
		node.prev.next = nil
	} else {
		node.prev.next = node.next
		node.next.prev = node.prev
	}
	delete(l.items, node.key)
	l.length--
	LFUNodePool.Put(node)
}

// addToFront adds the given node to the front of the linked list.
func (l *LFUAlgorithm) addToFront(node *Node) {
	node.next = l.list.head
	node.prev = nil
	if l.list.head != nil {
		l.list.head.prev = node
	}
	l.list.head = node
	if l.list.tail == nil {
		l.list.tail = node
	}
	l.items[node.key] = node
	l.length++
}

// moveToFront moves the given node to the front of the linked list.
func (l *LFUAlgorithm) moveToFront(node *Node) {
	if node == l.list.head {
		return
	}
	l.remove(node)
	l.addToFront(node)
}

// Delete removes the given key from the cache.
func (l *LFUAlgorithm) Delete(key string) {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	node, ok := l.items[key]
	if !ok {
		return
	}
	l.remove(node)
	delete(l.items, key)
	l.length--
	LFUNodePool.Put(node)
}
