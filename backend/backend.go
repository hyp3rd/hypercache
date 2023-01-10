package backend

import (
	"errors"

	"github.com/hyp3rd/hypercache/cache"
)

var (
	ErrInvalidBackendType = errors.New("invalid backend type")
)

type IBackendConstrain interface {
	InMemoryBackend
}

type IInMemoryBackend[T IBackendConstrain] interface {
	// IBackend[T] is the interface that must be implemented by cache backends.
	IBackend[T]
	// List the items in the cache that meet the specified criteria.
	List(options ...FilterOption[InMemoryBackend]) ([]*cache.CacheItem, error)
}

// Backend is the interface that must be implemented by cache backends.
type IBackend[T IBackendConstrain] interface {
	// Get retrieves the item with the given key from the cache.
	// If the key is not found in the cache, it returns nil.
	Get(key string) (item *cache.CacheItem, ok bool)

	// Set adds a new item to the cache.
	Set(item *cache.CacheItem) error

	// Remove deletes the item with the given key from the cache.
	Remove(key string)

	// Clear removes all items from the cache.
	Clear()

	// Capacity returns the maximum number of items that can be stored in the cache.
	Capacity() int

	// SetCapacity sets the maximum number of items that can be stored in the cache.
	SetCapacity(capacity int)

	// Size returns the number of items currently stored in the cache.
	Size() int
}

func NewBackend[T IBackendConstrain](backendType string, capacity int) (IBackend[T], error) {
	switch backendType {
	case "memory":
		return NewInMemoryBackend[T](capacity)
	// case "redis":
	// 	return NewRedisBackend(capacity, options...)
	// case "memcache":
	// 	return NewMemcacheBackend(capacity, options...)
	default:
		return nil, ErrInvalidBackendType
	}
}
