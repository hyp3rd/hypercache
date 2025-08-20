package backend

import (
	"context"
	"sync"

	cache "github.com/hyp3rd/hypercache/cache/v4"
	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/internal/sentinel"
	"github.com/hyp3rd/hypercache/types"
)

// InMemory is a cache backend that stores the items in memory, leveraging a custom `ConcurrentMap`.
type InMemory struct {
	sync.RWMutex // mutex to protect the cache from concurrent access

	items           cache.ConcurrentMap    // map to store the items in the cache
	itemPoolManager *types.ItemPoolManager // item pool manager to manage the item pool
	capacity        int                    // capacity of the cache, limits the number of items that can be stored in the cache
}

// NewInMemory creates a new in-memory cache with the given options.
func NewInMemory(opts ...Option[InMemory]) (IBackend[InMemory], error) {
	InMemory := &InMemory{
		items:           cache.New(),
		itemPoolManager: types.NewItemPoolManager(),
	}
	// Apply the backend options
	ApplyOptions(InMemory, opts...)
	// Check if the `capacity` is valid
	if InMemory.capacity < 0 {
		return nil, sentinel.ErrInvalidCapacity
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
func (cacheBackend *InMemory) Count(_ context.Context) int {
	return cacheBackend.items.Count()
}

// Get retrieves the item with the given key from the cacheBackend. If the item is not found, it returns nil.
func (cacheBackend *InMemory) Get(_ context.Context, key string) (*types.Item, bool) {
	item, ok := cacheBackend.items.Get(key)
	if !ok {
		return nil, false
	}
	// return the item
	return item, true
}

// Set adds a Item to the cache.
func (cacheBackend *InMemory) Set(_ context.Context, item *types.Item) error {
	// Check for invalid key, value, or duration
	err := item.Valid()
	if err != nil {
		cacheBackend.itemPoolManager.Put(item)

		return err
	}

	cacheBackend.Lock()
	defer cacheBackend.Unlock()

	cacheBackend.items.Set(item.Key, item)

	return nil
}

// List returns a list of all items in the cache filtered and ordered by the given options.
func (cacheBackend *InMemory) List(_ context.Context, filters ...IFilter) ([]*types.Item, error) {
	// Apply the filters
	cacheBackend.RLock()
	defer cacheBackend.RUnlock()

	var err error

	items := make([]*types.Item, 0, cacheBackend.items.Count())

	for item := range cacheBackend.items.IterBuffered() {
		cloned := item
		items = append(items, &cloned.Val)
	}

	// Apply the filters
	if len(filters) > 0 {
		for _, filter := range filters {
			items, err = filter.ApplyFilter(constants.InMemoryBackend, items)
			if err != nil {
				return nil, err
			}
		}
	}

	return items, nil
}

// Remove removes items with the given key from the cacheBackend. If an item is not found, it does nothing.
func (cacheBackend *InMemory) Remove(ctx context.Context, keys ...string) error {
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
		return sentinel.ErrTimeoutOrCanceled
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
		return sentinel.ErrTimeoutOrCanceled
	}
}
