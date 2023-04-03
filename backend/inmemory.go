package backend

import (
	"context"
	"sync"

	datastructure "github.com/hyp3rd/hypercache/datastructure/v4"
	"github.com/hyp3rd/hypercache/errors"
	"github.com/hyp3rd/hypercache/types"
)

// InMemory is a cache backend that stores the items in memory, leveraging a custom `ConcurrentMap`.
type InMemory struct {
	items       datastructure.ConcurrentMap // map to store the items in the cache
	capacity    int                         // capacity of the cache, limits the number of items that can be stored in the cache
	mutex       sync.RWMutex                // mutex to protect the cache from concurrent access
	SortFilters                             // filters applied when listing the items in the cache
}

// NewInMemory creates a new in-memory cache with the given options.
func NewInMemory(opts ...Option[InMemory]) (backend IBackend[InMemory], err error) {
	InMemory := &InMemory{
		items: datastructure.New(),
	}
	// Apply the backend options
	ApplyOptions(InMemory, opts...)
	// Check if the `capacity` is valid
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

// Capacity returns the capacity of the cacheBackend.
func (cacheBackend *InMemory) Capacity() int {
	return cacheBackend.capacity
}

// Count returns the number of items in the cache.
func (cacheBackend *InMemory) Count() int {
	return cacheBackend.items.Count()
}

// Get retrieves the item with the given key from the cacheBackend. If the item is not found, it returns nil.
func (cacheBackend *InMemory) Get(key string) (item *types.Item, ok bool) {
	item, ok = cacheBackend.items.Get(key)
	if !ok {
		return nil, false
	}
	// return the item
	return item, true
}

// Set adds a Item to the cache.
func (cacheBackend *InMemory) Set(item *types.Item) error {
	// Check for invalid key, value, or duration
	if err := item.Valid(); err != nil {
		types.ItemPool.Put(item)
		return err
	}

	cacheBackend.mutex.Lock()
	defer cacheBackend.mutex.Unlock()

	cacheBackend.items.Set(item.Key, item)
	return nil
}

// List returns a list of all items in the cache filtered and ordered by the given options
// func (cacheBackend *InMemory) List(ctx context.Context, options ...FilterOption[InMemory]) ([]*types.Item, error) {
func (cacheBackend *InMemory) List(ctx context.Context, filters ...IFilter) ([]*types.Item, error) {
	// Apply the filters
	cacheBackend.mutex.RLock()
	defer cacheBackend.mutex.RUnlock()

	items := make([]*types.Item, 0, cacheBackend.Count())
	wg := sync.WaitGroup{}
	wg.Add(cacheBackend.items.Count())
	for item := range cacheBackend.items.IterBuffered() {
		go func(item datastructure.Tuple) {
			defer wg.Done()
			if cacheBackend.FilterFunc == nil || cacheBackend.FilterFunc(&item.Val) {
				items = append(items, &item.Val)
			}
		}(item)
	}
	wg.Wait()

	for _, filter := range filters {
		items = filter.ApplyFilter("in-memory", items)
	}

	return items, nil
}

// Remove removes items with the given key from the cacheBackend. If an item is not found, it does nothing.
func (cacheBackend *InMemory) Remove(keys ...string) (err error) {
	//TODO: determine if handling the error or not
	// var ok bool
	// item := types.ItemPool.Get().(*types.Item)
	// defer types.ItemPool.Put(item)
	for _, key := range keys {
		cacheBackend.items.Remove(key)
	}
	return
}

// Clear removes all items from the cacheBackend.
func (cacheBackend *InMemory) Clear() error {
	// clear the cacheBackend
	cacheBackend.items.Clear()

	return nil
}
