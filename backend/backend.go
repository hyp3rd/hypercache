package backend

import (
	"context"

	"github.com/hyp3rd/hypercache/types"
)

// IBackendConstrain is the interface that defines the constrain type that must be implemented by cache backends.
// type IBackendConstrain interface {
// 	InMemory | Redis
// }

// // IInMemory is the interface that must be implemented by in-memory cache backends.
// type IInMemory[T IBackendConstrain] interface {
// 	// IBackend[T] is the interface that must be implemented by cache backends.
// 	IBackend[T]
// 	// List the items in the cache that meet the specified criteria.
// 	List(options ...FilterOption[InMemory]) ([]*types.Item, error)
// 	// Clear removes all items from the cache.
// 	Clear()
// }

// // IRedisBackend is the interface that must be implemented by Redis cache backends.
// type IRedisBackend[T IBackendConstrain] interface {
// 	// IBackend[T] is the interface that must be implemented by cache backends.
// 	IBackend[T]
// 	// List the items in the cache that meet the specified criteria.
// 	List(ctx context.Context, options ...FilterOption[Redis]) ([]*types.Item, error)
// 	// Clear removes all items from the cache.
// 	Clear() error
// }

// // IBackend is the interface that must be implemented by cache backends.
// type IBackend[T IBackendConstrain] interface {
// 	// Get retrieves the item with the given key from the cache.
// 	// If the key is not found in the cache, it returns nil.
// 	Get(key string) (item *types.Item, ok bool)
// 	// Set adds a new item to the cache.
// 	Set(item *types.Item) error
// 	// Capacity returns the maximum number of items that can be stored in the cache.
// 	Capacity() int
// 	// SetCapacity sets the maximum number of items that can be stored in the cache.
// 	SetCapacity(capacity int)
// 	// Count returns the number of items currently stored in the cache.
// 	Count() int
// 	// Remove deletes the item with the given key from the cache.
// 	Remove(keys ...string) error
// }

// IBackendConstrain is the interface that defines the constrain type that must be implemented by cache backends.
type IBackendConstrain interface {
	InMemory | Redis
}

// IBackend is the interface that must be implemented by cache backends.
type IBackend[T IBackendConstrain] interface {
	// Get retrieves the item with the given key from the cache.
	// If the key is not found in the cache, it returns nil.
	Get(key string) (item *types.Item, ok bool)
	// Set adds a new item to the cache.
	Set(item *types.Item) error
	// Capacity returns the maximum number of items that can be stored in the cache.
	Capacity() int
	// SetCapacity sets the maximum number of items that can be stored in the cache.
	SetCapacity(capacity int)
	// Count returns the number of items currently stored in the cache.
	Count() int
	// Remove deletes the item with the given key from the cache.
	Remove(keys ...string) error
	// List the items in the cache that meet the specified criteria.
	// List(ctx context.Context, options ...FilterOption[T]) ([]*types.Item, error)
	List(ctx context.Context, filters ...IFilter) ([]*types.Item, error)
	// Clear removes all items from the cache.
	Clear() error
}
