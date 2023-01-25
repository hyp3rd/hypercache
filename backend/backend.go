package backend

import (
	"github.com/hyp3rd/hypercache/errors"
	"github.com/hyp3rd/hypercache/models"
)

// IBackendConstrain is the interface that defines the constrain type that must be implemented by cache backends.
type IBackendConstrain interface {
	InMemory | Redis
}

// IInMemory is the interface that must be implemented by in-memory cache backends.
type IInMemory[T IBackendConstrain] interface {
	// IBackend[T] is the interface that must be implemented by cache backends.
	IBackend[T]
	// List the items in the cache that meet the specified criteria.
	List(options ...FilterOption[InMemory]) ([]*models.Item, error)
	// Clear removes all items from the cache.
	Clear()
}

// IRedisBackend is the interface that must be implemented by Redis cache backends.
type IRedisBackend[T IBackendConstrain] interface {
	// IBackend[T] is the interface that must be implemented by cache backends.
	IBackend[T]
	// List the items in the cache that meet the specified criteria.
	List(options ...FilterOption[Redis]) ([]*models.Item, error)
	// Clear removes all items from the cache.
	Clear() error
}

// IBackend is the interface that must be implemented by cache backends.
type IBackend[T IBackendConstrain] interface {
	// Get retrieves the item with the given key from the cache.
	// If the key is not found in the cache, it returns nil.
	Get(key string) (item *models.Item, ok bool)
	// Set adds a new item to the cache.
	Set(item *models.Item) error
	// Capacity returns the maximum number of items that can be stored in the cache.
	Capacity() int
	// SetCapacity sets the maximum number of items that can be stored in the cache.
	SetCapacity(capacity int)
	// Count returns the number of items currently stored in the cache.
	Count() int
	// Remove deletes the item with the given key from the cache.
	Remove(keys ...string) error
}

// NewBackend creates a new cache backend.
// Deprecated: Use specific backend constructors instead, e.g. NewInMemory or NewRedisBackend.
func NewBackend[T IBackendConstrain](backendType string, opts ...any) (IBackend[T], error) {
	switch backendType {
	case "memory":
		Options := make([]Option[InMemory], len(opts))
		for i, option := range opts {
			Options[i] = option.(Option[InMemory])
		}
		return NewInMemory(Options...)
	case "redis":
		Options := make([]Option[Redis], len(opts))
		for i, option := range opts {
			Options[i] = option.(Option[Redis])
		}
		return NewRedisBackend(Options...)
	default:
		return nil, errors.ErrInvalidBackendType
	}
}
