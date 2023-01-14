package backend

import (
	"github.com/hyp3rd/hypercache/cache"
	"github.com/hyp3rd/hypercache/errors"
)

// IBackendConstrain is the interface that defines the constrain type that must be implemented by cache backends.
type IBackendConstrain interface {
	InMemoryBackend | RedisBackend
}

// InMemoryBackend is the interface that must be implemented by in-memory cache backends.
type IInMemoryBackend[T IBackendConstrain] interface {
	// IBackend[T] is the interface that must be implemented by cache backends.
	IBackend[T]
	// List the items in the cache that meet the specified criteria.
	List(options ...FilterOption[InMemoryBackend]) ([]*cache.CacheItem, error)
	// Clear removes all items from the cache.
	Clear()
}

// RedisBackend is the interface that must be implemented by Redis cache backends.
type IRedisBackend[T IBackendConstrain] interface {
	// IBackend[T] is the interface that must be implemented by cache backends.
	IBackend[T]
	// List the items in the cache that meet the specified criteria.
	List(options ...FilterOption[RedisBackend]) ([]*cache.CacheItem, error)
	// Clear removes all items from the cache.
	Clear() error
}

// Backend is the interface that must be implemented by cache backends.
type IBackend[T IBackendConstrain] interface {
	// Get retrieves the item with the given key from the cache.
	// If the key is not found in the cache, it returns nil.
	Get(key string) (item *cache.CacheItem, ok bool)
	// Set adds a new item to the cache.
	Set(item *cache.CacheItem) error
	// Capacity returns the maximum number of items that can be stored in the cache.
	Capacity() int
	// SetCapacity sets the maximum number of items that can be stored in the cache.
	SetCapacity(capacity int)
	// Size returns the number of items currently stored in the cache.
	Size() int
	// Remove deletes the item with the given key from the cache.
	Remove(keys ...string) error
}

// Deprecated: NewBackend creates a new cache backend.
func NewBackend[T IBackendConstrain](backendType string, opts ...any) (IBackend[T], error) {
	switch backendType {
	case "memory":
		backendOptions := make([]BackendOption[InMemoryBackend], len(opts))
		for i, option := range opts {
			backendOptions[i] = option.(BackendOption[InMemoryBackend])
		}
		return NewInMemoryBackend(backendOptions...)
	case "redis":
		backendOptions := make([]BackendOption[RedisBackend], len(opts))
		for i, option := range opts {
			backendOptions[i] = option.(BackendOption[RedisBackend])
		}
		return NewRedisBackend(backendOptions...)
	default:
		return nil, errors.ErrInvalidBackendType
	}
}
