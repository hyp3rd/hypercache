package backend

import (
	"context"
	"sync"

	datastructure "github.com/hyp3rd/hypercache/cache/v4"
	"github.com/hyp3rd/hypercache/errors"
	"github.com/hyp3rd/hypercache/types"
)

// InMemory is a cache backend that stores the items in memory, leveraging a custom `ConcurrentMap`.
type InMemory struct {
	sync.RWMutex // mutex to protect the cache from concurrent access

	items    datastructure.ConcurrentMap // map to store the items in the cache
	capacity int                         // capacity of the cache, limits the number of items that can be stored in the cache
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
	err := item.Valid()
	if err != nil {
		types.ItemPool.Put(item)

		return err
	}

	cacheBackend.Lock()
	defer cacheBackend.Unlock()

	cacheBackend.items.Set(item.Key, item)

	return nil
}

// List returns a list of all items in the cache filtered and ordered by the given options.
func (cacheBackend *InMemory) List(ctx context.Context, filters ...IFilter) (items []*types.Item, err error) {
	// Apply the filters
	cacheBackend.RLock()
	defer cacheBackend.RUnlock()

	items = make([]*types.Item, 0, cacheBackend.items.Count())

	for item := range cacheBackend.items.IterBuffered() {
		copy := item
		items = append(items, &copy.Val)
	}

	// Apply the filters
	if len(filters) > 0 {
		for _, filter := range filters {
			items, err = filter.ApplyFilter("in-memory", items)
		}
	}

	return items, err
}

// Remove removes items with the given key from the cacheBackend. If an item is not found, it does nothing.
func (cacheBackend *InMemory) Remove(ctx context.Context, keys ...string) (err error) {
	done := make(chan struct{})

	go func() {
		defer close(done)

		for _, key := range keys {
			cacheBackend.items.Remove(key)
		}
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return errors.ErrTimeoutOrCanceled
	}
}

// Clear removes all items from the cacheBackend.
func (cacheBackend *InMemory) Clear(ctx context.Context) error {
	done := make(chan struct{})

	go func() {
		defer close(done)
		// clear the cacheBackend
		cacheBackend.items.Clear()
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return errors.ErrTimeoutOrCanceled
	}
}
