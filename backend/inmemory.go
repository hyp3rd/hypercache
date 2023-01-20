package backend

import (
	"fmt"
	"sort"
	"sync"

	"github.com/hyp3rd/hypercache/datastructure"
	"github.com/hyp3rd/hypercache/errors"
	"github.com/hyp3rd/hypercache/models"
	"github.com/hyp3rd/hypercache/types"
)

// InMemory is a cache backend that stores the items in memory, leveraging a custom `ConcurrentMap`.
type InMemory struct {
	items       datastructure.ConcurrentMap[string, *models.Item] // map to store the items in the cache
	capacity    int                                               // capacity of the cache, limits the number of items that can be stored in the cache
	mutex       sync.RWMutex                                      // mutex to protect the cache from concurrent access
	SortFilters                                                   // filters applied when listing the items in the cache
}

// NewInMemory creates a new in-memory cache with the given options.
func NewInMemory[T InMemory](opts ...Option[InMemory]) (backend IInMemory[T], err error) {

	InMemory := &InMemory{
		items: datastructure.New[*models.Item](),
	}

	ApplyOptions(InMemory, opts...)

	if InMemory.capacity < 0 {
		return nil, errors.ErrInvalidCapacity
	}

	return InMemory, nil
}

// SetCapacity sets the capacity of the cache.
func (cacheBackend *InMemory) SetCapacity(capacity int) {
	if capacity < 0 {
		return
	}

	cacheBackend.capacity = capacity
}

// itemCount returns the number of items in the cache.
func (cacheBackend *InMemory) itemCount() int {
	return cacheBackend.items.Count()
}

// Get retrieves the item with the given key from the cacheBackend. If the item is not found, it returns nil.
func (cacheBackend *InMemory) Get(key string) (item *models.Item, ok bool) {
	item, ok = cacheBackend.items.Get(key)
	if !ok {
		return nil, false
	}

	// return the item
	return item, true
}

// Set adds a Item to the cache.
func (cacheBackend *InMemory) Set(item *models.Item) error {
	// Check for invalid key, value, or duration
	if err := item.Valid(); err != nil {
		models.ItemPool.Put(item)
		return err
	}

	cacheBackend.mutex.Lock()
	defer cacheBackend.mutex.Unlock()

	cacheBackend.items.Set(item.Key, item)
	return nil
}

// List returns a list of all items in the cache filtered and ordered by the given options
func (cacheBackend *InMemory) List(options ...FilterOption[InMemory]) ([]*models.Item, error) {
	// Apply the filter options
	ApplyFilterOptions(cacheBackend, options...)

	items := make([]*models.Item, 0)
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
func (cacheBackend *InMemory) Remove(keys ...string) (err error) {
	//TODO: determine if handling the error or not
	for _, key := range keys {
		cacheBackend.items.Remove(key)
	}
	return
}

// Clear removes all items from the cacheBackend.
func (cacheBackend *InMemory) Clear() {
	for item := range cacheBackend.items.IterBuffered() {
		cacheBackend.items.Remove(item.Key)
	}
}

// Capacity returns the capacity of the cacheBackend.
func (cacheBackend *InMemory) Capacity() int {
	return cacheBackend.capacity
}

// Size returns the number of items in the cacheBackend.
func (cacheBackend *InMemory) Size() int {
	return cacheBackend.itemCount()
}
