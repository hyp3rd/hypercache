package eviction

import (
	"sync"

	"github.com/hyp3rd/hypercache/internal/sentinel"
)

type arcNode struct {
	key   string
	value any
	prev  *arcNode
	next  *arcNode
}

type arcGhostNode struct {
	key  string
	prev *arcGhostNode
	next *arcGhostNode
}

type arcList struct {
	head *arcNode
	tail *arcNode
	len  int
}

func (l *arcList) pushFront(node *arcNode) {
	node.prev = nil

	node.next = l.head
	if l.head != nil {
		l.head.prev = node
	}

	l.head = node
	if l.tail == nil {
		l.tail = node
	}

	l.len++
}

func (l *arcList) remove(node *arcNode) {
	switch {
	case l.head == l.tail:
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
	l.len--
}

func (l *arcList) removeTail() *arcNode {
	if l.tail == nil {
		return nil
	}

	t := l.tail
	l.remove(t)

	return t
}

type arcGhostList struct {
	head *arcGhostNode
	tail *arcGhostNode
	len  int
}

func (l *arcGhostList) pushFront(node *arcGhostNode) {
	node.prev = nil

	node.next = l.head
	if l.head != nil {
		l.head.prev = node
	}

	l.head = node
	if l.tail == nil {
		l.tail = node
	}

	l.len++
}

func (l *arcGhostList) remove(node *arcGhostNode) {
	switch {
	case l.head == l.tail:
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
	l.len--
}

func (l *arcGhostList) removeTail() *arcGhostNode {
	if l.tail == nil {
		return nil
	}

	t := l.tail
	l.remove(t)

	return t
}

// ARC implements the Adaptive Replacement Cache (resident T1/T2 and ghost B1/B2).
type ARC struct {
	mutex    sync.Mutex
	capacity int
	p        int // target size for T1

	// resident lists
	t1 arcList
	t2 arcList

	// ghost lists
	b1 arcGhostList
	b2 arcGhostList

	// indexes
	t1Idx map[string]*arcNode
	t2Idx map[string]*arcNode
	b1Idx map[string]*arcGhostNode
	b2Idx map[string]*arcGhostNode

	length int // |T1| + |T2|
}

// NewARCAlgorithm creates a new ARC with capacity.
func NewARCAlgorithm(capacity int) (*ARC, error) {
	if capacity < 0 {
		return nil, sentinel.ErrInvalidCapacity
	}

	return &ARC{
		capacity: capacity,
		p:        0,
		t1Idx:    make(map[string]*arcNode, capacity),
		t2Idx:    make(map[string]*arcNode, capacity),
		b1Idx:    make(map[string]*arcGhostNode, capacity),
		b2Idx:    make(map[string]*arcGhostNode, capacity),
	}, nil
}

// Get returns the value and updates ARC state.
func (a *ARC) Get(key string) (any, bool) {
	a.mutex.Lock()
	defer a.mutex.Unlock()

	if node, ok := a.t1Idx[key]; ok {
		// move to T2
		a.t1.remove(node)
		delete(a.t1Idx, key)
		a.t2.pushFront(node)

		a.t2Idx[key] = node

		return node.value, true
	}

	if node, ok := a.t2Idx[key]; ok {
		// refresh in T2
		a.t2.remove(node)
		a.t2.pushFront(node)

		return node.value, true
	}

	return nil, false
}

// Set inserts or updates a key according to ARC rules.
func (a *ARC) Set(key string, value any) {
	a.mutex.Lock()
	defer a.mutex.Unlock()

	if a.capacity == 0 {
		return
	}

	if a.updateIfResident(key, value) {
		return
	}

	if a.handleGhostHit(key, value) {
		return
	}

	a.insertNew(key, value)
}

// Delete removes key from ARC (resident or ghost).
func (a *ARC) Delete(key string) {
	a.mutex.Lock()
	defer a.mutex.Unlock()

	if node, ok := a.t1Idx[key]; ok {
		a.t1.remove(node)
		delete(a.t1Idx, key)

		a.length--

		return
	}

	if node, ok := a.t2Idx[key]; ok {
		a.t2.remove(node)
		delete(a.t2Idx, key)

		a.length--

		return
	}

	if ghost, ok := a.b1Idx[key]; ok {
		a.b1.remove(ghost)
		delete(a.b1Idx, key)

		return
	}

	if ghost, ok := a.b2Idx[key]; ok {
		a.b2.remove(ghost)
		delete(a.b2Idx, key)

		return
	}
}

// Evict selects a victim according to ARC policy.
func (a *ARC) Evict() (string, bool) {
	a.mutex.Lock()
	defer a.mutex.Unlock()

	if a.capacity == 0 || a.length == 0 {
		return "", false
	}

	key := a.replace("")
	if key == "" {
		return "", false
	}

	return key, true
}

// replace evicts one resident from T1 or T2 and places its key into B1 or B2.
// Returns the evicted resident key (if any).
func (a *ARC) replace(x string) string {
	fromT1 := a.t1.len > 0 && ((x != "" && a.b2Idx[x] != nil && a.t1.len == a.p) || (a.t1.len > a.p))
	if fromT1 {
		if tail := a.t1.removeTail(); tail != nil {
			delete(a.t1Idx, tail.key)
			a.b1.pushFront(&arcGhostNode{key: tail.key})

			a.b1Idx[tail.key] = a.b1.head
			a.length--

			return tail.key
		}
	} else {
		if tail := a.t2.removeTail(); tail != nil {
			delete(a.t2Idx, tail.key)
			a.b2.pushFront(&arcGhostNode{key: tail.key})

			a.b2Idx[tail.key] = a.b2.head
			a.length--

			return tail.key
		}
	}

	return ""
}

// updateIfResident updates value and placement for keys already in T1 or T2.
func (a *ARC) updateIfResident(key string, value any) bool {
	if node, ok := a.t1Idx[key]; ok {
		node.value = value
		a.t1.remove(node)
		delete(a.t1Idx, key)

		a.t2.pushFront(node)

		a.t2Idx[key] = node

		return true
	}

	if node, ok := a.t2Idx[key]; ok {
		node.value = value
		a.t2.remove(node)
		a.t2.pushFront(node)

		return true
	}

	return false
}

// handleGhostHit processes B1/B2 hits and moves the key to T2 while adapting p.
func (a *ARC) handleGhostHit(key string, value any) bool {
	if ghost, ok := a.b1Idx[key]; ok {
		increment := max(a.b2.len/arcIntMax(1, a.b1.len), 1)

		a.p += increment
		if a.p > a.capacity {
			a.p = a.capacity
		}

		a.replace(key)

		a.b1.remove(ghost)
		delete(a.b1Idx, key)

		node := &arcNode{key: key, value: value}
		a.t2.pushFront(node)

		a.t2Idx[key] = node
		a.length++

		return true
	}

	if ghost, ok := a.b2Idx[key]; ok {
		decrement := max(a.b1.len/arcIntMax(1, a.b2.len), 1)

		a.p -= decrement
		if a.p < 0 {
			a.p = 0
		}

		a.replace(key)

		a.b2.remove(ghost)
		delete(a.b2Idx, key)

		node := &arcNode{key: key, value: value}
		a.t2.pushFront(node)

		a.t2Idx[key] = node
		a.length++

		return true
	}

	return false
}

// insertNew ensures capacity and history bounds, then inserts key into T1.
func (a *ARC) insertNew(key string, value any) {
	if a.length >= a.capacity {
		a.ensureCapacityForNew()
	}

	node := &arcNode{key: key, value: value}
	a.t1.pushFront(node)

	a.t1Idx[key] = node
	a.length++
}

// ensureCapacityForNew frees space for a new resident item per ARC rules.
func (a *ARC) ensureCapacityForNew() {
	if a.t1.len+a.b1.len >= a.capacity {
		a.trimForHistoryLimit()

		return
	}

	a.trimForTotalLimit()
}

// trimForHistoryLimit handles the case where |T1| + |B1| >= c.
func (a *ARC) trimForHistoryLimit() {
	if a.t1.len < a.capacity {
		if tail := a.b1.removeTail(); tail != nil {
			delete(a.b1Idx, tail.key)
		}

		return
	}

	if tail := a.t1.removeTail(); tail != nil {
		delete(a.t1Idx, tail.key)
		a.b1.pushFront(&arcGhostNode{key: tail.key})

		a.b1Idx[tail.key] = a.b1.head
		a.length--
	}
}

// trimForTotalLimit handles the case where total lists exceed 2c, then calls replace.
func (a *ARC) trimForTotalLimit() {
	total := a.t1.len + a.t2.len + a.b1.len + a.b2.len
	if total >= 2*a.capacity {
		if tail := a.b2.removeTail(); tail != nil {
			delete(a.b2Idx, tail.key)
		}
	}

	a.replace("")
}

func arcIntMax(a, b int) int {
	if a > b {
		return a
	}

	return b
}
