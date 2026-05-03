package hypercache

import (
	"context"
	"time"

	"github.com/hyp3rd/hypercache/internal/sentinel"
	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// Set adds an item to the cache with the given key and value. If an item with the same key already
// exists, it updates the value. If the expiration duration is greater than zero, the item will
// expire after the specified duration.
//
// If capacity is reached:
//   - when evictionInterval == 0 we evict immediately
//   - otherwise the background eviction loop will reclaim space
func (hyperCache *HyperCache[T]) Set(ctx context.Context, key string, value any, expiration time.Duration) error {
	// Create a new cache item and set its properties
	item := hyperCache.itemPoolManager.Get()

	item.Key = key
	item.Value = value
	item.Expiration = expiration
	item.LastAccess = time.Now()

	// Set the size of the item (aligned with backend serializer when available)
	err := hyperCache.setItemSize(item)
	if err != nil {
		return err
	}

	// check if adding this item will exceed the maxCacheSize
	hyperCache.memoryAllocation.Add(item.Size)

	if hyperCache.maxCacheSize > 0 && hyperCache.memoryAllocation.Load() > hyperCache.maxCacheSize {
		hyperCache.memoryAllocation.Add(-item.Size)

		return sentinel.ErrCacheFull
	}

	// Insert the item into the cache
	err = hyperCache.backend.Set(ctx, item)
	if err != nil {
		hyperCache.memoryAllocation.Add(-item.Size)
		hyperCache.itemPoolManager.Put(item)

		return err
	}

	// Set the item in the eviction algorithm. The algorithm protects its own
	// state internally; no outer lock needed.
	hyperCache.evictionAlgorithm.Set(key, item.Value)

	// If the cache is at capacity, evict an item when the eviction interval is zero
	if hyperCache.shouldEvict.Load() && hyperCache.backend.Count(ctx) > hyperCache.backend.Capacity() {
		hyperCache.evictItem(ctx)
	}

	return nil
}

// Get retrieves the item with the given key from the cache returning the value and a boolean indicating if the item was found.
func (hyperCache *HyperCache[T]) Get(ctx context.Context, key string) (any, bool) {
	item, ok := hyperCache.backend.Get(ctx, key)
	if !ok {
		return nil, false
	}

	// Check if the item has expired, if so, trigger the expiration loop
	if item.Expired() {
		// Non-blocking trigger of expiration loop (do not return to pool yet; backend still holds it)
		// Coalesced/debounced trigger
		hyperCache.execTriggerExpiration()

		return nil, false
	}

	// Update the last access time and access count
	hyperCache.touchItem(ctx, key, item)

	return item.Value, true
}

// GetWithInfo retrieves the item with the given key from the cache returning the `Item` object and a boolean indicating if the item was
// found.
func (hyperCache *HyperCache[T]) GetWithInfo(ctx context.Context, key string) (*cache.Item, bool) {
	item, ok := hyperCache.backend.Get(ctx, key)
	// Check if the item has expired if it exists, if so, trigger the expiration loop
	if !ok {
		return nil, false
	}

	// Check if the item has expired, if so, trigger the expiration loop
	if item.Expired() {
		// Non-blocking trigger of expiration loop; don't return to pool here
		// Coalesced/debounced trigger
		hyperCache.execTriggerExpiration()

		return nil, false
	}

	// Update the last access time and access count
	hyperCache.touchItem(ctx, key, item)

	return item, true
}

// GetOrSet retrieves the item with the given key. If the item is not found, it adds the item to the cache with the given value and
// expiration duration.
// If the capacity of the cache is reached, leverage the eviction algorithm.
func (hyperCache *HyperCache[T]) GetOrSet(ctx context.Context, key string, value any, expiration time.Duration) (any, error) {
	// if the item is found, return the value
	if item, ok := hyperCache.backend.Get(ctx, key); ok {
		// Check if the item has expired
		if item.Expired() {
			// Non-blocking trigger of expiration loop; don't pool here to avoid zeroing live refs
			// Coalesced/debounced trigger
			hyperCache.execTriggerExpiration()

			return nil, sentinel.ErrKeyExpired
		}

		// Update the last access time and access count
		hyperCache.touchItem(ctx, key, item)

		return item.Value, nil
	}

	// if the item is not found, add it to the cache
	item := hyperCache.itemPoolManager.Get()

	item.Key = key
	item.Value = value
	item.Expiration = expiration
	item.LastAccess = time.Now()

	// Set the size of the item (aligned with backend serializer when available)
	err := hyperCache.setItemSize(item)
	if err != nil {
		return nil, err
	}

	// check if adding this item will exceed the maxCacheSize
	hyperCache.memoryAllocation.Add(item.Size)

	if hyperCache.maxCacheSize > 0 && hyperCache.memoryAllocation.Load() > hyperCache.maxCacheSize {
		hyperCache.memoryAllocation.Add(-item.Size)
		hyperCache.itemPoolManager.Put(item)

		return nil, sentinel.ErrCacheFull
	}

	// Insert the item into the cache
	err = hyperCache.backend.Set(ctx, item)
	if err != nil {
		hyperCache.memoryAllocation.Add(-item.Size)
		hyperCache.itemPoolManager.Put(item)

		return nil, err
	}

	// Set the item in the eviction algorithm synchronously. The previous bare
	// goroutine had no panic recovery, no shutdown coordination with Stop(),
	// and let the caller observe a key whose eviction tracking had not yet
	// been recorded. The algorithm's own internal mutex provides safety.
	hyperCache.evictionAlgorithm.Set(key, item.Value)

	// If the cache is at capacity, evict an item when the eviction interval is zero
	if hyperCache.shouldEvict.Load() && hyperCache.backend.Count(ctx) > hyperCache.backend.Capacity() {
		hyperCache.evictItem(ctx)
	}

	return value, nil
}

// GetMultiple retrieves the items with the given keys from the cache.
func (hyperCache *HyperCache[T]) GetMultiple(ctx context.Context, keys ...string) (map[string]any, map[string]error) {
	result := make(map[string]any, len(keys))   // Preallocate the result map
	failed := make(map[string]error, len(keys)) // Preallocate the errors map

	for _, key := range keys {
		item, ok := hyperCache.backend.Get(ctx, key)
		if !ok {
			// Add the key to the errors map and continue
			failed[key] = sentinel.ErrKeyNotFound

			continue
		}

		// Check if the item has expired
		if item.Expired() {
			// Treat expired items as not found per API semantics; don't pool here to avoid zeroing live refs
			failed[key] = sentinel.ErrKeyNotFound
			// Coalesced/debounced trigger of the expiration loop via channel
			hyperCache.execTriggerExpiration()
		} else {
			hyperCache.touchItem(ctx, key, item) // Update the last access time and access count

			// Add the item to the result map
			result[key] = item.Value
		}
	}

	return result, failed
}

// touchItem records an access on the backend (when supported) and on the item itself.
func (hyperCache *HyperCache[T]) touchItem(ctx context.Context, key string, item *cache.Item) {
	if item == nil {
		return
	}

	if toucher, ok := hyperCache.backend.(touchBackend); ok {
		toucher.Touch(ctx, key)
	}

	item.Touch()
}

// List lists the items in the cache that meet the specified criteria.
// It takes in a variadic number of any type as filters, it then checks the backend type, and calls the corresponding
// implementation of the List function for that backend, with the filters passed in as arguments.
func (hyperCache *HyperCache[T]) List(ctx context.Context, filters ...backend.IFilter) ([]*cache.Item, error) {
	return hyperCache.backend.List(ctx, filters...)
}

// setItemSize computes item.Size using the backend serializer when available for accuracy.
// Falls back to the Item's internal SetSize when no serializer is present.
func (hyperCache *HyperCache[T]) setItemSize(item *cache.Item) error {
	// Prefer backend-specific serialization for accurate size accounting.
	switch backendImpl := any(hyperCache.backend).(type) {
	case *backend.Redis:
		if backendImpl.Serializer != nil {
			data, err := backendImpl.Serializer.Marshal(item.Value)
			if err != nil {
				return err
			}

			item.Size = int64(len(data))

			return nil
		}

	case *backend.RedisCluster:
		if backendImpl.Serializer != nil {
			data, err := backendImpl.Serializer.Marshal(item.Value)
			if err != nil {
				return err
			}

			item.Size = int64(len(data))

			return nil
		}

	default:
		// Fall back to generic size estimation for backends without a serializer.
	}

	return item.SetSize()
}

// Remove removes items with the given key from the cache. If an item is not found, it does nothing.
func (hyperCache *HyperCache[T]) Remove(ctx context.Context, keys ...string) error {
	// Remove the item from the eviction algorithm
	// and update the memory allocation
	for _, key := range keys {
		item, ok := hyperCache.backend.Get(ctx, key)
		if ok {
			// remove the item from the cacheBackend and update the memory allocation
			hyperCache.memoryAllocation.Add(-item.Size)
			hyperCache.evictionAlgorithm.Delete(key)
		}
	}

	err := hyperCache.backend.Remove(ctx, keys...)
	if err != nil {
		return err
	}

	return nil
}

// Clear removes all items from the cache.
func (hyperCache *HyperCache[T]) Clear(ctx context.Context) error {
	var (
		items []*cache.Item
		err   error
	)

	// get all expired items
	items, err = hyperCache.backend.List(ctx)
	if err != nil {
		return err
	}

	// clear the cacheBackend
	err = hyperCache.backend.Clear(ctx)
	if err != nil {
		return err
	}

	for _, item := range items {
		hyperCache.evictionAlgorithm.Delete(item.Key)
	}

	// reset the memory allocation
	hyperCache.memoryAllocation.Store(0)

	return err
}
