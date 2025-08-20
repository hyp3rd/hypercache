// Package backend provides interfaces and types for implementing cache backends.
// It defines the contract that all cache backends must follow, including operations
// for storing, retrieving, and managing cached items. The package supports generic
// backend implementations with type constraints to ensure type safety.
//
// The main interface IBackend provides methods for:
//   - Getting and setting cache items
//   - Managing cache capacity and item count
//   - Removing items and clearing the cache
//   - Listing items with optional filters
//
// Backend implementations must satisfy the IBackendConstrain type constraint,
// which currently supports InMemory and Redis backend types.
package backend

import (
	"context"

	"github.com/hyp3rd/hypercache/pkg/cache"
)

// IBackendConstrain defines the type constraint for cache backend implementations.
// It restricts the generic type parameter to supported backend types, ensuring
// type safety and proper implementation at compile time.
type IBackendConstrain interface {
	InMemory | Redis
}

// IBackend defines the contract that all cache backends must implement.
// It provides a generic interface for cache operations with type safety
// through the IBackendConstrain type parameter.
//
// Type parameter T must satisfy IBackendConstrain, limiting implementations
// to supported backend types like InMemory and Redis.
//
// All methods accept a context.Context parameter for cancellation and timeout
// control, enabling graceful handling of long-running operations.
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
