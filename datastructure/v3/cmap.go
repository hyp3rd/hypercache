package v3

import (
	"hash"
	"hash/fnv"
	"sync"

	"github.com/hyp3rd/hypercache/models"
)

const (
	// ShardCount is the number of shards used by the map.
	ShardCount = 32
	// ShardCount32 is the number of shards used by the map pre-casted to uint32 to performace issues.
	ShardCount32 uint32 = uint32(ShardCount)
)

// ConcurrentMap is a "thread" safe map of type string:*models.Item.
// To avoid lock bottlenecks this map is dived to several (ShardCount) map shards.
type ConcurrentMap struct {
	shards []*ConcurrentMapShard
	hasher hash.Hash32
}

// ConcurrentMapShard is a "thread" safe string to `*models.Item`.
type ConcurrentMapShard struct {
	items map[string]*models.Item
	sync.RWMutex
}

// New creates a new concurrent map.
func New() ConcurrentMap {
	// h := hasherSyncPool.Get().(hash.Hash32)
	// defer hasherSyncPool.Put(h)
	return ConcurrentMap{
		shards: create(),
		// hasher: h,
		hasher: fnv.New32a(),
	}
}

func create() []*ConcurrentMapShard {
	shards := make([]*ConcurrentMapShard, ShardCount)
	for i := 0; i < ShardCount; i++ {
		shards[i] = &ConcurrentMapShard{items: make(map[string]*models.Item)}
	}
	return shards
}

// GetShard returns shard under given key
func (m *ConcurrentMap) GetShard(key string) *ConcurrentMapShard {
	// Calculate the shard index using a bitwise AND operation
	shardIndex := m.hasher.Sum32() & (ShardCount32 - 1)
	return m.shards[shardIndex]
}

// Set sets the given value under the specified key.
func (m *ConcurrentMap) Set(key string, value *models.Item) {
	shard := m.GetShard(key)
	shard.Lock()
	shard.items[key] = value
	shard.Unlock()
}

// Get retrieves an element from map under given key.
func (m *ConcurrentMap) Get(key string) (*models.Item, bool) {
	// Get shard
	shard := m.GetShard(key)
	shard.RLock()
	// Get item from shard.
	item, ok := shard.items[key]
	shard.RUnlock()
	return item, ok
}

// Has checks if key is present in the map.
func (m *ConcurrentMap) Has(key string) bool {
	// Get shard
	shard := m.GetShard(key)
	shard.RLock()
	// Get item from shard.
	_, ok := shard.items[key]
	shard.RUnlock()
	return ok
}

// Pop removes an element from the map and returns it.
func (m *ConcurrentMap) Pop(key string) (*models.Item, bool) {
	shard := m.GetShard(key)
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

// Tuple is used by the IterBuffered functions to wrap two variables together over a channel,
type Tuple struct {
	Key string
	Val models.Item
}

// IterBuffered returns a buffered iterator which could be used in a for range loop.
func (m ConcurrentMap) IterBuffered() <-chan Tuple {
	chans := snapshot(m)
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
func snapshot(m ConcurrentMap) (chans []chan Tuple) {
	//When you access map items before initializing.
	if len(m.shards) == 0 {
		panic(`cmap.ConcurrentMap is not initialized. Should run New() before usage.`)
	}
	chans = make([]chan Tuple, ShardCount)
	wg := sync.WaitGroup{}
	wg.Add(ShardCount)
	// Foreach shard.
	for index, shard := range m.shards {
		go func(index int, shard *ConcurrentMapShard) {
			// Foreach key, value pair.
			shard.RLock()
			chans[index] = make(chan Tuple, len(shard.items))
			wg.Done()
			for key, val := range shard.items {
				chans[index] <- Tuple{key, *val}
			}
			shard.RUnlock()
			close(chans[index])
		}(index, shard)
	}
	wg.Wait()
	return chans
}

// fanIn reads elements from channels `chans` into channel `out`
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
func (m *ConcurrentMap) Remove(key string) {
	// Get map shard.
	shard := m.GetShard(key)
	shard.Lock()
	delete(shard.items, key)
	shard.Unlock()
}

// Clear removes all items from map.
func (m ConcurrentMap) Clear() {
	for item := range m.IterBuffered() {
		m.Remove(item.Key)
	}
}

// Count returns the number of items in the map.
func (m *ConcurrentMap) Count() int {
	count := 0
	for _, shard := range m.shards {
		shard.RLock()
		count += len(shard.items)
		shard.RUnlock()
	}
	return count
}
