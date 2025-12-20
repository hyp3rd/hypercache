// Package v2 provides a high-performance concurrent map implementation optimized for cache operations.
// The implementation uses sharding to minimize lock contention by dividing the map into multiple
// independent shards, each protected by its own read-write mutex.
//
// The concurrent map stores string keys mapped to *cache.Item values and is designed to be
// thread-safe for concurrent read and write operations across multiple goroutines.
//
// Key features:
//   - Sharded design with 32 shards to reduce lock contention
//   - FNV-1a hash function for efficient key distribution
//   - Thread-safe operations with optimized read/write locking
//   - Buffered iteration support for safe concurrent traversal
//   - Standard map operations: Set, Get, Has, Remove, Pop, Clear, Count
//
// Example usage:
//
//	cm := v2.New()
//	cm.Set("key", &cache.Item{...})
//	if item, ok := cm.Get("key"); ok {
//	    // Process item
//	}
package v2

import (
	"sync"
)

const (
	// ShardCount is the number of shards used by the map.
	ShardCount = 32
	// ShardCount32 is the number of shards used by the map pre-casted to uint32 to avoid performance issues.
	ShardCount32 uint32 = uint32(ShardCount)
)

// ConcurrentMap is a "thread" safe map of type string:*cache.Item.
// To avoid lock bottlenecks this map is divided into several (ShardCount) map shards.
type ConcurrentMap struct {
	shards []*ConcurrentMapShard
}

// ConcurrentMapShard is a "thread" safe string to `*cache.Item` map shard.
type ConcurrentMapShard struct {
	sync.RWMutex

	items map[string]*Item
}

// New creates a new concurrent map.
func New() ConcurrentMap {
	return ConcurrentMap{
		shards: create(),
	}
}

// create initializes and returns an array of ConcurrentMapShard pointers.
func create() []*ConcurrentMapShard {
	shards := make([]*ConcurrentMapShard, ShardCount)
	for i := range ShardCount {
		shards[i] = &ConcurrentMapShard{
			items: make(map[string]*Item),
		}
	}

	return shards
}

// GetShard returns shard under given key.
func (cm *ConcurrentMap) GetShard(key string) *ConcurrentMapShard {
	shardIndex := getShardIndex(key)

	return cm.shards[shardIndex]
}

// getShardIndex calculates the shard index for the given key.
func getShardIndex(key string) uint32 {
	// Inline FNV-1a 32-bit hashing to avoid allocations.
	const (
		fnvOffset32 = 2166136261
		fnvPrime32  = 16777619
	)

	var sum uint32 = fnvOffset32
	for i := range key { // Go 1.22+ integer range over string indices
		sum ^= uint32(key[i])

		sum *= fnvPrime32
	}

	// Calculate the shard index using a bitwise AND operation.
	return sum & (ShardCount32 - 1)
}

// Set sets the given value under the specified key.
func (cm *ConcurrentMap) Set(key string, value *Item) {
	shard := cm.GetShard(key)
	shard.Lock()

	shard.items[key] = value
	shard.Unlock()
}

// Get retrieves an element from map under given key.
func (cm *ConcurrentMap) Get(key string) (*Item, bool) {
	// Get shard
	shard := cm.GetShard(key)
	shard.RLock()
	// Get item from shard.
	item, ok := shard.items[key]
	shard.RUnlock()

	return item, ok
}

// Has checks if key is present in the map.
func (cm *ConcurrentMap) Has(key string) bool {
	// Get shard
	shard := cm.GetShard(key)
	shard.RLock()
	// Get item from shard.
	_, ok := shard.items[key]
	shard.RUnlock()

	return ok
}

// Pop removes an element from the map and returns it.
func (cm *ConcurrentMap) Pop(key string) (*Item, bool) {
	shard := cm.GetShard(key)
	shard.Lock()

	item, ok := shard.items[key]
	if !ok {
		shard.Unlock()

		return nil, false
	}

	delete(shard.items, key)
	shard.Unlock()

	return item, ok
}

// Tuple is used by the IterBuffered functions to wrap two variables together over a channel,.
type Tuple struct {
	Key string
	Val Item
}

// IterBuffered returns a buffered iterator which could be used in a for range loop.
func (cm *ConcurrentMap) IterBuffered() <-chan Tuple {
	chans := snapshot(cm)

	total := 0
	for _, c := range chans {
		total += cap(c)
	}

	ch := make(chan Tuple, total)
	go fanIn(chans, ch)

	return ch
}

// Returns a array of channels that contains elements in each shard,
// which likely takes a snapshot of `m`.
// It returns once the size of each buffered channel is determined,
// before all the channels are populated using goroutines.
func snapshot(cm *ConcurrentMap) []chan Tuple {
	// When you access map items before initializing.
	if len(cm.shards) == 0 {
		panic(`cmap.ConcurrentMap is not initialized. Should run New() before usage.`)
	}

	chans := make([]chan Tuple, ShardCount)
	wg := sync.WaitGroup{}
	wg.Add(ShardCount)
	// Foreach shard.
	for index, shard := range cm.shards {
		go func(index int, shard *ConcurrentMapShard) {
			// Foreach key, value pair.
			shard.RLock()
			// Determine capacity and copy to a local slice to shorten lock hold time.
			n := len(shard.items)

			chans[index] = make(chan Tuple, n)

			local := make([]Tuple, 0, n)
			for key, val := range shard.items {
				local = append(local, Tuple{Key: key, Val: *val})
			}

			shard.RUnlock()

			wg.Done()

			for _, t := range local {
				chans[index] <- t
			}

			close(chans[index])
		}(index, shard)
	}

	wg.Wait()

	return chans
}

// fanIn reads elements from channels `chans` into channel `out`.
func fanIn(chans []chan Tuple, out chan Tuple) {
	wg := sync.WaitGroup{}
	wg.Add(len(chans))

	for _, ch := range chans {
		go func(ch chan Tuple) {
			for t := range ch {
				out <- t
			}

			wg.Done()
		}(ch)
	}

	wg.Wait()
	close(out)
}

// Remove removes the value under the specified key.
func (cm *ConcurrentMap) Remove(key string) {
	// Get map shard.
	shard := cm.GetShard(key)
	shard.Lock()
	delete(shard.items, key)
	shard.Unlock()
}

// Clear removes all items from map.
func (cm *ConcurrentMap) Clear() {
	// Fast clear: reset each shard's map under lock.
	for _, shard := range cm.shards {
		shard.Lock()

		shard.items = make(map[string]*Item)
		shard.Unlock()
	}
}

// Count returns the number of items in the map.
func (cm *ConcurrentMap) Count() int {
	count := 0

	for _, shard := range cm.shards {
		shard.RLock()

		count += len(shard.items)
		shard.RUnlock()
	}

	return count
}
