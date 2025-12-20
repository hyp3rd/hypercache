// Package cache provides a thread-safe concurrent map implementation with sharding
// for improved performance in high-concurrency scenarios.
package cache

import (
	"fmt"
	"sync"

	"github.com/goccy/go-json"
	"github.com/hyp3rd/ewrap"
)

// ShardCount is the number of shards.
const (
	ShardCount        = 32
	prime32    uint32 = 16777619
	offset32   uint32 = 2166136261
)

// Stringer is the interface implemented by any value that has a String method,.
type Stringer interface {
	fmt.Stringer
	comparable
}

// ConcurrentMap is a "thread" safe map of type string:Anything.
// To avoid lock bottlenecks this map is dived to several (ShardCount) map shards.
type ConcurrentMap[K comparable, V any] struct {
	shards   []*ConcurrentMapShared[K, V]
	sharding func(key K) uint32
}

// ConcurrentMapShared is a "thread" safe string to anything map.
type ConcurrentMapShared[K comparable, V any] struct {
	sync.RWMutex // Read Write mutex, guards access to internal map.

	items map[K]V
}

func create[K comparable, V any](sharding func(key K) uint32) ConcurrentMap[K, V] {
	cmap := ConcurrentMap[K, V]{
		sharding: sharding,
		shards:   make([]*ConcurrentMapShared[K, V], ShardCount),
	}
	for i := range ShardCount {
		cmap.shards[i] = &ConcurrentMapShared[K, V]{items: make(map[K]V)}
	}

	return cmap
}

// New creates a new concurrent map.
func New[V any]() ConcurrentMap[string, V] {
	return create[string, V](fnv32)
}

// NewStringer creates a new concurrent map.
func NewStringer[K Stringer, V any]() ConcurrentMap[K, V] {
	return create[K, V](strfnv32[K])
}

// NewWithCustomShardingFunction creates a new concurrent map.
func NewWithCustomShardingFunction[K comparable, V any](sharding func(key K) uint32) ConcurrentMap[K, V] {
	return create[K, V](sharding)
}

// GetShard returns shard under given key.
func (m ConcurrentMap[K, V]) GetShard(key K) *ConcurrentMapShared[K, V] {
	return m.shards[uint(m.sharding(key))%uint(ShardCount)]
}

// MSet Sets the given value under the specified key.
func (m ConcurrentMap[K, V]) MSet(data map[K]V) {
	for key, value := range data {
		shard := m.GetShard(key)
		shard.Lock()

		shard.items[key] = value
		shard.Unlock()
	}
}

// Set Sets the given value under the specified key.
func (m ConcurrentMap[K, V]) Set(key K, value V) {
	// Get map shard.
	shard := m.GetShard(key)
	shard.Lock()

	shard.items[key] = value
	shard.Unlock()
}

// UpsertCb callback to return new element to be inserted into the map
// It is called while lock is held, therefore it MUST NOT
// try to access other keys in same map, as it can lead to deadlock since
// Go sync.RWLock is not reentrant.
type UpsertCb[V any] func(exist bool, valueInMap, newValue V) V

// Upsert Insert or Update - updates existing element or inserts a new one using UpsertCb.
func (m ConcurrentMap[K, V]) Upsert(key K, value V, cb UpsertCb[V]) V {
	shard := m.GetShard(key)
	shard.Lock()

	v, ok := shard.items[key]
	res := cb(ok, v, value)

	shard.items[key] = res
	shard.Unlock()

	return res
}

// SetIfAbsent sets the given value under the specified key if no value was associated with it.
func (m ConcurrentMap[K, V]) SetIfAbsent(key K, value V) bool {
	// Get map shard.
	shard := m.GetShard(key)
	shard.Lock()

	_, ok := shard.items[key]
	if !ok {
		shard.items[key] = value
	}

	shard.Unlock()

	return !ok
}

// Get retrieves an element from map under given key.
func (m ConcurrentMap[K, V]) Get(key K) (V, bool) {
	// Get shard
	shard := m.GetShard(key)
	shard.RLock()
	// Get item from shard.
	val, ok := shard.items[key]
	shard.RUnlock()

	return val, ok
}

// Count returns the number of elements within the map.
func (m ConcurrentMap[K, V]) Count() int {
	count := 0

	for i := range ShardCount {
		shard := m.shards[i]
		shard.RLock()

		count += len(shard.items)
		shard.RUnlock()
	}

	return count
}

// Has looks up an item under specified key.
func (m ConcurrentMap[K, V]) Has(key K) bool {
	// Get shard
	shard := m.GetShard(key)
	shard.RLock()
	// See if element is within shard.
	_, ok := shard.items[key]
	shard.RUnlock()

	return ok
}

// Remove removes an element from the map.
func (m ConcurrentMap[K, V]) Remove(key K) error {
	// Try to get shard.
	shard := m.GetShard(key)
	if shard == nil {
		return ewrap.New("key not found")
	}

	shard.Lock()
	delete(shard.items, key)
	shard.Unlock()

	return nil
}

// RemoveCb is a callback executed in a map.RemoveCb() call, while Lock is held
// If returns true, the element will be removed from the map.
type RemoveCb[K any, V any] func(key K, v V, exists bool) bool

// RemoveCb locks the shard containing the key, retrieves its current value and calls the callback with those params
// If callback returns true and element exists, it will remove it from the map
// Returns the value returned by the callback (even if element was not present in the map).
func (m ConcurrentMap[K, V]) RemoveCb(key K, cb RemoveCb[K, V]) bool {
	// Try to get shard.
	shard := m.GetShard(key)
	shard.Lock()

	v, ok := shard.items[key]

	remove := cb(key, v, ok)
	if remove && ok {
		delete(shard.items, key)
	}

	shard.Unlock()

	return remove
}

// Pop removes an element from the map and returns it.
func (m ConcurrentMap[K, V]) Pop(key K) (V, bool) {
	// Try to get shard.
	shard := m.GetShard(key)
	shard.Lock()

	v, exists := shard.items[key]
	delete(shard.items, key)
	shard.Unlock()

	return v, exists
}

// IsEmpty checks if map is empty.
func (m ConcurrentMap[K, V]) IsEmpty() bool {
	return m.Count() == 0
}

// Tuple is used by the Iter & IterBuffered functions to wrap two variables together over a channel,.
type Tuple[K comparable, V any] struct {
	Key K
	Val V
}

// IterBuffered returns a buffered iterator which could be used in a for range loop.
func (m ConcurrentMap[K, V]) IterBuffered() <-chan Tuple[K, V] {
	chans := snapshot(m)

	total := 0
	for _, c := range chans {
		total += cap(c)
	}

	ch := make(chan Tuple[K, V], total)
	go fanIn(chans, ch)

	return ch
}

// Clear removes all items from map.
func (m ConcurrentMap[K, V]) Clear() error {
	eg := ewrap.NewErrorGroup()

	for item := range m.IterBuffered() {
		err := m.Remove(item.Key)
		if err != nil {
			eg.Add(err)
		}
	}

	return eg.ErrorOrNil()
}

// Returns a array of channels that contains elements in each shard,
// which likely takes a snapshot of `m`.
// It returns once the size of each buffered channel is determined,
// before all the channels are populated using goroutines.
func snapshot[Key comparable, Val any](cmap ConcurrentMap[Key, Val]) []chan Tuple[Key, Val] {
	// When you access map items before initializing.
	if len(cmap.shards) == 0 {
		panic(`cmap.ConcurrentMap is not initialized. Should run New() before usage.`)
	}

	chans := make([]chan Tuple[Key, Val], ShardCount)
	wg := sync.WaitGroup{}
	wg.Add(ShardCount)
	// Foreach shard.
	for index, shard := range cmap.shards {
		go func(index int, shard *ConcurrentMapShared[Key, Val]) {
			// Foreach key, value pair.
			shard.RLock()

			chans[index] = make(chan Tuple[Key, Val], len(shard.items))

			wg.Done()

			for key, val := range shard.items {
				chans[index] <- Tuple[Key, Val]{key, val}
			}

			shard.RUnlock()
			close(chans[index])
		}(index, shard)
	}

	wg.Wait()

	return chans
}

// fanIn reads elements from channels `chans` into channel `out`.
func fanIn[K comparable, V any](chans []chan Tuple[K, V], out chan Tuple[K, V]) {
	wg := sync.WaitGroup{}
	wg.Add(len(chans))

	for _, ch := range chans {
		go func(ch chan Tuple[K, V]) {
			for t := range ch {
				out <- t
			}

			wg.Done()
		}(ch)
	}

	wg.Wait()
	close(out)
}

// Items returns all items as map[string]V.
func (m ConcurrentMap[K, V]) Items() map[K]V {
	tmp := make(map[K]V)

	// Insert items to temporary map.
	for item := range m.IterBuffered() {
		tmp[item.Key] = item.Val
	}

	return tmp
}

// IterCb is the iterator calledback for every key,value found in
// maps. RLock is held for all calls for a given shard
// therefore callback sess consistent view of a shard,
// but not across the shards.
type IterCb[K comparable, V any] func(key K, v V)

// IterCb is a callback based iterator, cheapest way to read
// all elements in a map.
func (m ConcurrentMap[K, V]) IterCb(fn IterCb[K, V]) {
	for idx := range m.shards {
		shard := (m.shards)[idx]
		shard.RLock()

		for key, value := range shard.items {
			fn(key, value)
		}

		shard.RUnlock()
	}
}

// Keys returns all keys as []string.
func (m ConcurrentMap[K, V]) Keys() []K {
	count := m.Count()
	ch := make(chan K, count)

	go func() {
		// Foreach shard.
		wg := sync.WaitGroup{}
		wg.Add(ShardCount)

		for _, shard := range m.shards {
			go func(shard *ConcurrentMapShared[K, V]) {
				// Foreach key, value pair.
				shard.RLock()

				for key := range shard.items {
					ch <- key
				}

				shard.RUnlock()
				wg.Done()
			}(shard)
		}

		wg.Wait()
		close(ch)
	}()

	// Generate keys
	keys := make([]K, 0, count)
	for k := range ch {
		keys = append(keys, k)
	}

	return keys
}

// MarshalJSON reviles ConcurrentMap "private" variables to json marshal.
func (m ConcurrentMap[K, V]) MarshalJSON() ([]byte, error) {
	// Create a temporary map, which will hold all item spread across shards.
	tmp := make(map[K]V)

	// Insert items to temporary map.
	for item := range m.IterBuffered() {
		tmp[item.Key] = item.Val
	}

	data, err := json.Marshal(tmp)
	if err != nil {
		return nil, ewrap.Wrap(err, "failed to marshal json")
	}

	return data, nil
}

// Returns a hash for a key.
func strfnv32[K fmt.Stringer](key K) uint32 {
	return fnv32(key.String())
}

// Returns a hash for a string.
func fnv32(key string) uint32 {
	hash := offset32

	keyLength := len(key)
	for i := range keyLength {
		hash *= prime32

		hash ^= uint32(key[i])
	}

	return hash
}

// UnmarshalJSON reverse process of Marshal.
func (m *ConcurrentMap[K, V]) UnmarshalJSON(b []byte) error {
	tmp := make(map[K]V)

	// Unmarshal into a single map.
	err := json.Unmarshal(b, &tmp)
	if err != nil {
		return ewrap.Wrap(err, "failed to unmarshal json")
	}

	// foreach key,value pair in temporary map insert into our concurrent map.
	for key, val := range tmp {
		m.Set(key, val)
	}

	return nil
}
