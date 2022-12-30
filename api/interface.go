package api

import (
	"github.com/hyp3rd/hypercache"
)

// CacheAPI is an interface for managing the cache.
type CacheAPI interface {
	// SetCapacity sets the capacity of the cache. If the new capacity is less than the current cache size, it evicts items to bring the cache size down to the new capacity.
	SetCapacity(capacity int) error

	// Set adds a new value to the cache with the given key and options.
	Set(key string, value interface{}, options ...hypercache.CacheItemOption) error

	// Get retrieves the value of a cache item with the given key. It returns the value and a boolean indicating whether the value was found in the cache.
	Get(key string) (interface{}, bool)

	// GetOrSet retrieves the value of a cache item with the given key, or sets a new value in the cache if the key doesn't exist.
	// It returns the value and a boolean indicating whether the value was retrieved from the cache (true) or set in the cache (false).
	GetOrSet(key string, value interface{}, options ...hypercache.CacheItemOption) (interface{}, bool)

	// Evict evicts items from the cache. It removes the specified number of items from the end of the lru list,
	// up to the number of items needed to bring the cache size down to the capacity.
	Evict(count int)

	// Stats returns the cache statistics.
	Stats() any
}
