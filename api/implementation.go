// Copyright 2023 F. All rights reserved.
// Use of this source code is governed by a Mozilla Public License 2.0
// license that can be found in the LICENSE file.
// HyperCache API is an in-memory cache implementation in Go that supports the expiration and eviction of items.
// Use the NewCache function to create a new cache with the given capacity and options, and use the CacheAPI interface to manage the cache.
// The cache is thread-safe and can be used concurrently by multiple goroutines, and is meant to use as a manager, not as a cache itself.
package api

import "github.com/hyp3rd/hypercache"

// NewCache creates a new in-memory cache with the given capacity and options.
// It is meant to use as a manager, not as a cache itself.
func NewCache(capacity int, options ...hypercache.Option) (cache *Cache, err error) {
	// Create a new HyperCache with the given capacity and options
	hc, err := hypercache.NewHyperCache(capacity, options...)
	if err != nil {
		return nil, err
	}

	// Return a new Cache that embeds the HyperCache
	return &Cache{hc}, nil
}

// Cache is a struct that embeds the HyperCache struct and implements the CacheAPI interface.
type Cache struct {
	*hypercache.HyperCache
}

// SetCapacity sets the capacity of the cache. If the new capacity is less than the current cache size, it evicts items to bring the cache size down to the new capacity.
func (c *Cache) SetCapacity(capacity int) error {
	c.HyperCache.SetCapacity(capacity)
	return nil
}

// Set adds a new value to the cache with the given key and options.
func (c *Cache) Set(key string, value interface{}, options ...hypercache.CacheItemOption) error {
	return c.HyperCache.SetWithOptions(key, value, options...)
}

// Get retrieves the value of a cache item with the given key. It returns the value and a boolean indicating whether the value was found in the cache.
func (c *Cache) Get(key string) (interface{}, bool) {
	return c.HyperCache.Get(key)
}

// GetOrSet retrieves the value of a cache item with the given key, or sets a new value in the cache if the key doesn't exist.
// It returns the value and a boolean indicating whether the value was retrieved from the cache (true) or set in the cache (false).
func (c *Cache) GetOrSet(key string, value interface{}, options ...hypercache.CacheItemOption) (interface{}, bool) {
	return c.HyperCache.GetOrSet(key, value, options...)
}

// Evict evicts items from the cache. It removes the specified number of items from the end of the lru list,
// up to the number of items needed to bring the cache size down to the capacity.
func (c *Cache) Evict(count int) {
	c.HyperCache.Evict(count)
}

// Stats returns the cache statistics.
func (c *Cache) Stats() any {
	return c.HyperCache.Stats()
}
