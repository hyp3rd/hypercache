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
//   - iter.Seq2 iteration via All() for safe concurrent traversal
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
	"iter"
	"sync"
	"sync/atomic"
	"time"
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
//
// count tracks len(items) under the same lock as items, but as an atomic so
// Count() (sum of 32 shard counts) can read it without acquiring any locks.
// This eliminates the lock-storm in the eviction inner loop's per-iteration
// Count() check.
type ConcurrentMapShard struct {
	sync.RWMutex

	items map[string]*Item
	count atomic.Int64
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
// Uses the shared Hash function so other packages (eviction.Sharded)
// route the same key to the same logical shard.
func getShardIndex(key string) uint32 {
	return Hash(key) & (ShardCount32 - 1)
}

// Set sets the given value under the specified key.
func (cm *ConcurrentMap) Set(key string, value *Item) {
	shard := cm.GetShard(key)
	shard.Lock()

	if _, existed := shard.items[key]; !existed {
		shard.count.Add(1)
	}

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

// GetCopy retrieves a copy of the item under the given key.
func (cm *ConcurrentMap) GetCopy(key string) (*Item, bool) {
	shard := cm.GetShard(key)
	shard.RLock()

	item, ok := shard.items[key]
	if !ok {
		shard.RUnlock()

		return nil, false
	}

	cloned := *item

	shard.RUnlock()

	return &cloned, true
}

// Touch updates the last access time and access count for a key.
func (cm *ConcurrentMap) Touch(key string) bool {
	shard := cm.GetShard(key)

	shard.Lock()
	defer shard.Unlock()

	item, ok := shard.items[key]
	if !ok {
		return false
	}

	item.LastAccess = time.Now()
	item.AccessCount++

	return true
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
	shard.count.Add(-1)
	shard.Unlock()

	return item, ok
}

// All returns an iter.Seq2 that yields every (key, *Item) pair across all
// shards. Each shard is walked under its own RLock, released before moving
// to the next shard — concurrent writers to a different shard are not
// blocked.
//
// IMPORTANT: the yielded *Item points directly into the live map. Callers
// MUST treat it as read-only and copy if they need to retain the value
// past the yield. The shard's RLock is held during yield, so a
// long-running consumer body will block writers to that shard. Drain into
// a local slice first if the consumer needs to do I/O or block.
//
// Replaces the previous IterBuffered channel-based iterator: no fan-in
// goroutines, no per-shard channel allocations.
func (cm *ConcurrentMap) All() iter.Seq2[string, *Item] {
	return func(yield func(string, *Item) bool) {
		if len(cm.shards) == 0 {
			panic(`cmap.ConcurrentMap is not initialized. Should run New() before usage.`)
		}

		for _, shard := range cm.shards {
			if !yieldShard(shard, yield) {
				return
			}
		}
	}
}

// yieldShard walks one shard under RLock and forwards entries to yield.
// Returns true if iteration should continue, false if the consumer asked
// to stop.
func yieldShard(shard *ConcurrentMapShard, yield func(string, *Item) bool) bool {
	shard.RLock()
	defer shard.RUnlock()

	for k, v := range shard.items {
		if !yield(k, v) {
			return false
		}
	}

	return true
}

// Remove removes the value under the specified key.
func (cm *ConcurrentMap) Remove(key string) {
	// Get map shard.
	shard := cm.GetShard(key)
	shard.Lock()

	if _, existed := shard.items[key]; existed {
		delete(shard.items, key)
		shard.count.Add(-1)
	}

	shard.Unlock()
}

// Clear removes all items from map.
func (cm *ConcurrentMap) Clear() {
	// Fast clear: reset each shard's map under lock.
	for _, shard := range cm.shards {
		shard.Lock()

		shard.items = make(map[string]*Item)
		shard.count.Store(0)
		shard.Unlock()
	}
}

// Count returns the number of items in the map.
//
// Lock-free: each shard maintains its cardinality as an atomic.Int64
// (mutated under the shard lock alongside items). Count() is the sum of
// the 32 atomics. The previous implementation walked all 32 shard RLocks
// per call, which serialized with writers and was the dominant cost in
// the eviction inner loop's `for backend.Count(ctx) > backend.Capacity()`.
func (cm *ConcurrentMap) Count() int {
	var total int64

	for _, shard := range cm.shards {
		total += shard.count.Load()
	}

	return int(total)
}
