package backend

import (
	"context"

	"github.com/hyp3rd/hypercache/pkg/cache"
)

// IBackendConstrain is the interface that defines the constrain type implemented by cache backends.
type IBackendConstrain interface {
	InMemory | Redis
}

// IBackend is the interface that must be implemented by cache backends.
type IBackend[T IBackendConstrain] interface {
	// Get retrieves the item with the given key from the cache.
	// If the key is not found in the cache, it returns nil.
	Get(ctx context.Context, key string) (item *cache.Item, ok bool)
	// Set adds a new item to the cache.
	Set(ctx context.Context, item *cache.Item) error
	// Capacity returns the maximum number of items that can be stored in the cache.
	Capacity() int
	// SetCapacity sets the maximum number of items that can be stored in the cache.
	SetCapacity(capacity int)
	// Count returns the number of items currently stored in the cache.
	Count(ctx context.Context) int
	// Remove deletes the item with the given key from the cache.
	Remove(ctx context.Context, keys ...string) error
	// List the items in the cache that meet the specified criteria.
	List(ctx context.Context, filters ...IFilter) (items []*cache.Item, err error)
	// Clear removes all items from the cache.
	Clear(ctx context.Context) error
}
