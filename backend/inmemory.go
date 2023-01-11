package backend

import (
	"sort"
	"sync"
	"time"

	"github.com/hyp3rd/hypercache/cache"
	"github.com/hyp3rd/hypercache/datastructure"
	"github.com/hyp3rd/hypercache/errors"
	"github.com/hyp3rd/hypercache/types"
)

// InMemoryBackend is a cache backend that stores the items in memory, leveraging a custom concurrent map.
type InMemoryBackend struct {
	items    datastructure.ConcurrentMap[string, *cache.CacheItem] // map to store the items in the cache
	capacity int                                                   // capacity of the cache, limits the number of items that can be stored in the cache
	mutex    sync.RWMutex                                          // mutex to protect the cache from concurrent access

	// sortBy is the field to sort the items by.
	// The field can be any of the fields in the `CacheItem` struct.
	sortBy string
	// sortAscending is a boolean indicating whether the items should be sorted in ascending order.
	// If set to false, the items will be sorted in descending order.
	sortAscending bool
	// filterFunc is a predicate that takes a `CacheItem` as an argument and returns a boolean indicating whether the item should be included in the cache.
	filterFunc func(item *cache.CacheItem) bool // filters applied when listing the items in the cache
}

// NewInMemoryBackend creates a new in-memory cache with the given capacity.
func NewInMemoryBackend[T InMemoryBackend](capacity int) (backend IInMemoryBackend[T], err error) {
	if capacity < 0 {
		return nil, errors.ErrInvalidCapacity
	}

	inMemoryBackend := &InMemoryBackend{
		items:    datastructure.New[*cache.CacheItem](),
		capacity: capacity,
	}

	return inMemoryBackend, nil
}

// SetCapacity sets the capacity of the cacheBackend. If the new capacity is smaller than the current number of items in the cache,
// it evicts the excess items from the cacheBackend.
func (cacheBackend *InMemoryBackend) SetCapacity(capacity int) {
	if capacity < 0 {
		return
	}

	cacheBackend.capacity = capacity
}

// itemCount returns the number of items in the cacheBackend.
func (cacheBackend *InMemoryBackend) itemCount() int {
	return cacheBackend.items.Count()
}

// Get retrieves the item with the given key from the cacheBackend. If the item is not found, it returns nil.
func (cacheBackend *InMemoryBackend) Get(key string) (item *cache.CacheItem, ok bool) {
	item, ok = cacheBackend.items.Get(key)
	if !ok {
		return nil, false
	}

	// return the item
	return item, true
}

// Set adds an item to the cache with the given key and value. If an item with the same key already exists, it updates the value of the existing item.
// If the expiration duration is greater than zero, the item will expire after the specified duration.
// If the capacity of the cache is reached, the cache will evict the least recently used item before adding the new item.
func (cacheBackend *InMemoryBackend) Set(item *cache.CacheItem) error {
	// Check for invalid key, value, or duration
	if err := item.Valid(); err != nil {
		cache.CacheItemPool.Put(item)
		return err
	}

	cacheBackend.mutex.Lock()
	defer cacheBackend.mutex.Unlock()

	cacheBackend.items.Set(item.Key, item)
	return nil
}

// List the items in the cache that meet the specified criteria.
func (cacheBackend *InMemoryBackend) List(options ...FilterOption[InMemoryBackend]) ([]*cache.CacheItem, error) {
	// Apply the filter options
	ApplyBackendOptions(cacheBackend, options...)

	items := make([]*cache.CacheItem, 0)
	for item := range cacheBackend.items.IterBuffered() {
		if cacheBackend.filterFunc == nil || cacheBackend.filterFunc(item.Val) {
			items = append(items, item.Val)
		}
	}

	if cacheBackend.sortBy == "" {
		return items, nil
	}

	sort.Slice(items, func(i, j int) bool {
		a := items[i].FieldByName(cacheBackend.sortBy)
		b := items[j].FieldByName(cacheBackend.sortBy)
		switch cacheBackend.sortBy {
		case types.SortByKey.String():
			if cacheBackend.sortAscending {
				return a.Interface().(string) < b.Interface().(string)
			}
			return a.Interface().(string) > b.Interface().(string)
		case types.SortByValue.String():
			if cacheBackend.sortAscending {
				return a.Interface().(string) < b.Interface().(string)
			}
			return a.Interface().(string) > b.Interface().(string)
		case types.SortByLastAccess.String():
			if cacheBackend.sortAscending {
				return a.Interface().(time.Time).Before(b.Interface().(time.Time))
			}
			return a.Interface().(time.Time).After(b.Interface().(time.Time))
		case types.SortByAccessCount.String():
			if cacheBackend.sortAscending {
				return a.Interface().(uint) < b.Interface().(uint)
			}
			return a.Interface().(uint) > b.Interface().(uint)
		case types.SortByExpiration.String():
			if cacheBackend.sortAscending {
				return a.Interface().(time.Duration) < b.Interface().(time.Duration)
			}
			return a.Interface().(time.Duration) > b.Interface().(time.Duration)
		default:
			return false
		}
	})

	return items, nil
}

// Remove removes items with the given key from the cacheBackend. If an item is not found, it does nothing.
func (cacheBackend *InMemoryBackend) Remove(keys ...string) (err error) {
	//TODO: determine if handling the error or not
	for _, key := range keys {
		cacheBackend.items.Remove(key)
	}
	return
}

// Clear removes all items from the cacheBackend.
func (cacheBackend *InMemoryBackend) Clear() {
	for item := range cacheBackend.items.IterBuffered() {
		cacheBackend.items.Remove(item.Key)
	}
}

// Capacity returns the capacity of the cacheBackend.
func (cacheBackend *InMemoryBackend) Capacity() int {
	return cacheBackend.capacity
}

// Size returns the number of items in the cacheBackend.
func (cacheBackend *InMemoryBackend) Size() int {
	return cacheBackend.itemCount()
}
