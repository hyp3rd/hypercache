package backend

import (
	"fmt"
	"sort"
	"sync"

	"github.com/hyp3rd/hypercache/cache"
	"github.com/hyp3rd/hypercache/datastructure"
	"github.com/hyp3rd/hypercache/errors"
	"github.com/hyp3rd/hypercache/types"
)

// InMemoryBackend is a cache backend that stores the items in memory, leveraging a custom `ConcurrentMap`.
type InMemoryBackend struct {
	items       datastructure.ConcurrentMap[string, *cache.CacheItem] // map to store the items in the cache
	capacity    int                                                   // capacity of the cache, limits the number of items that can be stored in the cache
	mutex       sync.RWMutex                                          // mutex to protect the cache from concurrent access
	SortFilters                                                       // filters applied when listing the items in the cache
}

// NewInMemoryBackend creates a new in-memory cache with the given options.
func NewInMemoryBackend[T InMemoryBackend](opts ...BackendOption[InMemoryBackend]) (backend IInMemoryBackend[T], err error) {

	inMemoryBackend := &InMemoryBackend{
		items: datastructure.New[*cache.CacheItem](),
	}

	ApplyBackendOptions(inMemoryBackend, opts...)

	if inMemoryBackend.capacity < 0 {
		return nil, errors.ErrInvalidCapacity
	}

	return inMemoryBackend, nil
}

// SetCapacity sets the capacity of the cache.
func (cacheBackend *InMemoryBackend) SetCapacity(capacity int) {
	if capacity < 0 {
		return
	}

	cacheBackend.capacity = capacity
}

// itemCount returns the number of items in the cache.
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

// Set adds a CacheItem to the cache.
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
// func (cacheBackend *InMemoryBackend) List(options ...FilterOption[InMemoryBackend]) ([]*cache.CacheItem, error) {
// 	// Apply the filter options
// 	ApplyBackendOptions(cacheBackend, options...)

// 	items := make([]*cache.CacheItem, 0)
// 	for item := range cacheBackend.items.IterBuffered() {
// 		if cacheBackend.filterFunc == nil || cacheBackend.filterFunc(item.Val) {
// 			items = append(items, item.Val)
// 		}
// 	}

// 	if cacheBackend.sortBy == "" {
// 		return items, nil
// 	}

// 	sort.Slice(items, func(i, j int) bool {
// 		a := items[i].FieldByName(cacheBackend.sortBy)
// 		b := items[j].FieldByName(cacheBackend.sortBy)
// 		switch cacheBackend.sortBy {
// 		case types.SortByKey.String():
// 			if cacheBackend.sortAscending {
// 				return a.Interface().(string) < b.Interface().(string)
// 			}
// 			return a.Interface().(string) > b.Interface().(string)
// 		case types.SortByValue.String():
// 			if cacheBackend.sortAscending {
// 				return a.Interface().(string) < b.Interface().(string)
// 			}
// 			return a.Interface().(string) > b.Interface().(string)
// 		case types.SortByLastAccess.String():
// 			if cacheBackend.sortAscending {
// 				return a.Interface().(time.Time).Before(b.Interface().(time.Time))
// 			}
// 			return a.Interface().(time.Time).After(b.Interface().(time.Time))
// 		case types.SortByAccessCount.String():
// 			if cacheBackend.sortAscending {
// 				return a.Interface().(uint) < b.Interface().(uint)
// 			}
// 			return a.Interface().(uint) > b.Interface().(uint)
// 		case types.SortByExpiration.String():
// 			if cacheBackend.sortAscending {
// 				return a.Interface().(time.Duration) < b.Interface().(time.Duration)
// 			}
// 			return a.Interface().(time.Duration) > b.Interface().(time.Duration)
// 		default:
// 			return false
// 		}
// 	})

// 	return items, nil
// }

// func (cacheBackend *InMemoryBackend) List(options ...FilterOption[InMemoryBackend]) ([]*cache.CacheItem, error) {
// 	// Apply the filter options
// 	ApplyBackendOptions(cacheBackend, options...)

// 	items := make([]*cache.CacheItem, 0)
// 	for item := range cacheBackend.items.IterBuffered() {
// 		if cacheBackend.filterFunc == nil || cacheBackend.filterFunc(item.Val) {
// 			items = append(items, item.Val)
// 		}
// 	}

// 	if cacheBackend.sortBy == "" {
// 		return items, nil
// 	}

// 	switch cacheBackend.sortBy {
// 	case types.SortByKey.String():
// 		sort.Sort(ByKey{items, cacheBackend.sortAscending})
// 	case types.SortByValue.String():
// 		// compare := func(x, y *cache.CacheItem) bool {
// 		// 	// custom compare function
// 		// 	return x.Value.(string) < y.Value.(string)
// 		// }
// 		// if cacheBackend.sortAscending {
// 		// 	sort.Slice(items, func(i, j int) bool { return compare(items[i], items[j]) })
// 		// } else {
// 		// 	sort.Slice(items, func(i, j int) bool { return !compare(items[i], items[j]) })
// 		// }
// 		compare := func(x, y *cache.CacheItem) bool {
// 			// Compare values
// 			return cmp.Equal(x.Value, y.Value)
// 		}
// 		// sort the items using cmp.Sort
// 		cmp.Sort(items, compare)
// 	case types.SortByLastAccess.String():
// 		sort.Sort(ByLastAccess{items, cacheBackend.sortAscending})
// 	case types.SortByAccessCount.String():
// 		sort.Sort(ByAccessCount{items, cacheBackend.sortAscending})
// 	case types.SortByExpiration.String():
// 		sort.Sort(ByExpiration{items, cacheBackend.sortAscending})
// 	}

// 	return items, nil
// }

// List returns a list of all items in the cache filtered and ordered by the given options
func (cacheBackend *InMemoryBackend) List(options ...FilterOption[InMemoryBackend]) ([]*cache.CacheItem, error) {
	// Apply the filter options
	ApplyFilterOptions(cacheBackend, options...)

	items := make([]*cache.CacheItem, 0)
	for item := range cacheBackend.items.IterBuffered() {
		if cacheBackend.FilterFunc == nil || cacheBackend.FilterFunc(item.Val) {
			items = append(items, item.Val)
		}
	}

	if cacheBackend.SortBy == "" {
		return items, nil
	}

	var sorter sort.Interface
	switch cacheBackend.SortBy {
	case types.SortByKey.String():
		sorter = &itemSorterByKey{items: items}
	case types.SortByValue.String():
		sorter = &itemSorterByValue{items: items}
	case types.SortByLastAccess.String():
		sorter = &itemSorterByLastAccess{items: items}
	case types.SortByAccessCount.String():
		sorter = &itemSorterByAccessCount{items: items}
	case types.SortByExpiration.String():
		sorter = &itemSorterByExpiration{items: items}
	default:
		return nil, fmt.Errorf("unknown sortBy field: %s", cacheBackend.SortBy)
	}

	if !cacheBackend.SortAscending {
		sorter = sort.Reverse(sorter)
	}

	sort.Sort(sorter)
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
