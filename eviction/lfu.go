package eviction

// Least Frequently Used (LFU) eviction algorithm implementation

import (
	"container/heap"
	"sync"

	"github.com/hyp3rd/hypercache/errors"
)

// LFUAlgorithm is an eviction algorithm that uses the Least Frequently Used (LFU) policy to select items for eviction.
type LFUAlgorithm struct {
	items  map[string]*Node
	freqs  *FrequencyHeap
	mutex  sync.RWMutex
	length int
	cap    int
}

// Node is a node in the LFUAlgorithm.
type Node struct {
	key   string
	value any
	count int
	index int
}

// FrequencyHeap is a heap of Nodes.
type FrequencyHeap []*Node

// Len returns the length of the heap.
func (fh FrequencyHeap) Len() int { return len(fh) }

// Less returns true if the node at index i has a lower frequency than the node at index j.
func (fh FrequencyHeap) Less(i, j int) bool {
	if fh[i].count == fh[j].count {
		return fh[i].index < fh[j].index
	}

	return fh[i].count < fh[j].count
}

// Swap swaps the nodes at index i and j.
func (fh FrequencyHeap) Swap(i, j int) {
	fh[i], fh[j] = fh[j], fh[i]
	fh[i].index = i
	fh[j].index = j
}

// Push adds a node to the heap.
func (fh *FrequencyHeap) Push(x any) {
	n := len(*fh)
	node := x.(*Node)
	node.index = n
	*fh = append(*fh, node)
}

// Pop removes the last node from the heap.
func (fh *FrequencyHeap) Pop() any {
	old := *fh
	n := len(old)
	node := old[n-1]
	node.index = -1
	*fh = old[0 : n-1]

	return node
}

// NewLFUAlgorithm creates a new LFUAlgorithm with the given capacity.
func NewLFUAlgorithm(capacity int) (*LFUAlgorithm, error) {
	if capacity < 0 {
		return nil, errors.ErrInvalidCapacity
	}

	return &LFUAlgorithm{
		items:  make(map[string]*Node, capacity),
		freqs:  &FrequencyHeap{},
		length: 0,
		cap:    capacity,
	}, nil
}

// Evict evicts an item from the cache based on the LFU algorithm.
func (l *LFUAlgorithm) Evict() (string, bool) {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	return l.internalEvict()
}

// Set sets a key-value pair in the cache.
func (l *LFUAlgorithm) Set(key string, value any) {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	if l.length == l.cap {
		_, _ = l.internalEvict()
	}

	node := &Node{
		key:   key,
		value: value,
		count: 1,
	}
	l.items[key] = node
	heap.Push(l.freqs, node)
	l.length++
}

// Get gets a value from the cache.
func (l *LFUAlgorithm) Get(key string) (any, bool) {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	node, ok := l.items[key]
	if !ok {
		return nil, false
	}

	node.count++
	heap.Fix(l.freqs, node.index)

	return node.value, true
}

// Delete deletes a key-value pair from the cache.
func (l *LFUAlgorithm) Delete(key string) {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	node, ok := l.items[key]
	if !ok {
		return
	}

	heap.Remove(l.freqs, node.index)

	for i := node.index; i < len(*l.freqs); i++ {
		(*l.freqs)[i].index--
	}

	delete(l.items, key)
	l.length--
}

// internalEvict evicts an item from the cache based on the LFU algorithm.
func (l *LFUAlgorithm) internalEvict() (string, bool) {
	if l.length == 0 {
		return "", false
	}

	node := heap.Pop(l.freqs).(*Node)
	delete(l.items, node.key)
	l.length--

	return node.key, true
}
