package eviction

import (
	"container/heap"
	"sync"

	"github.com/hyp3rd/hypercache/internal/sentinel"
)

// LFUAlgorithm is an eviction algorithm that uses the Least Frequently Used (LFU) policy to select items for eviction.
type LFUAlgorithm struct {
	items  map[string]*Node
	freqs  *FrequencyHeap
	mutex  sync.RWMutex
	length int
	cap    int
	seq    uint64 // monotonic sequence to break frequency ties by recency (LRU on ties)
}

// Node is a node in the LFUAlgorithm.
type Node struct {
	key   string
	value any
	count int
	index int
	last  uint64 // last access sequence (higher = more recent)
}

// FrequencyHeap is a heap of Nodes.
//
//nolint:recvcheck
type FrequencyHeap []*Node

// Len returns the length of the heap.
func (fh FrequencyHeap) Len() int { return len(fh) }

// Less returns true if the node at index i has a lower frequency than the node at index j.
func (fh FrequencyHeap) Less(i, j int) bool {
	if fh[i].count == fh[j].count {
		// On ties, evict the least recently used (older last sequence has priority)
		return fh[i].last < fh[j].last
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

	node, ok := x.(*Node)
	if ok {
		node.index = n
		*fh = append(*fh, node)
	}
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
		return nil, sentinel.ErrInvalidCapacity
	}

	return &LFUAlgorithm{
		items:  make(map[string]*Node, capacity),
		freqs:  &FrequencyHeap{},
		length: 0,
		cap:    capacity,
		seq:    0,
	}, nil
}

// Evict evicts an item from the cache based on the LFU algorithm.
func (l *LFUAlgorithm) Evict() (string, bool) {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	if l.cap == 0 {
		return "", false
	}

	return l.internalEvict()
}

// Set sets a key-value pair in the cache.
func (l *LFUAlgorithm) Set(key string, value any) {
	l.mutex.Lock()
	defer l.mutex.Unlock()

	if l.cap == 0 {
		// Zero-capacity LFU is a no-op
		return
	}

	if node, ok := l.items[key]; ok {
		// Key exists: update value and increment frequency
		node.value = value
		node.count++

		l.seq++

		node.last = l.seq
		heap.Fix(l.freqs, node.index)

		return
	}

	if l.length == l.cap {
		_, _ = l.internalEvict()
	}

	l.seq++

	node := &Node{
		key:   key,
		value: value,
		count: 1,
		last:  l.seq,
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

	l.seq++

	node.last = l.seq
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

	// heap.Remove will maintain heap invariants and update indices
	heap.Remove(l.freqs, node.index)
	delete(l.items, key)

	l.length--
}

// internalEvict evicts an item from the cache based on the LFU algorithm.
func (l *LFUAlgorithm) internalEvict() (string, bool) {
	if l.length == 0 {
		return "", false
	}

	node, ok := heap.Pop(l.freqs).(*Node)
	if !ok {
		return "", false
	}

	delete(l.items, node.key)

	l.length--

	return node.key, true
}
