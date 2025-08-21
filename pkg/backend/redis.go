package backend

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/hyp3rd/ewrap"

	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/internal/libs/serializer"
	"github.com/hyp3rd/hypercache/internal/sentinel"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

const (
	maxRetries   = 3
	retriesDelay = 100 * time.Millisecond
)

// Redis is a cache backend that stores the items in a redis implementation.
type Redis struct {
	rdb             *redis.Client          // redis client to interact with the redis server
	capacity        int                    // capacity of the cache, limits the number of items that can be stored in the cache
	itemPoolManager *cache.ItemPoolManager // itemPoolManager is used to manage the item pool for memory efficiency
	keysSetName     string                 // keysSetName is the name of the set that holds the keys of the items in the cache
	Serializer      serializer.ISerializer // Serializer is the serializer used to serialize the items before storing them in the cache
}

// NewRedis creates a new redis cache with the given options.
func NewRedis(redisOptions ...Option[Redis]) (IBackend[Redis], error) {
	rb := &Redis{
		itemPoolManager: cache.NewItemPoolManager(),
	}
	// Apply the backend options
	ApplyOptions(rb, redisOptions...)

	// Check if the client is nil
	if rb.rdb == nil {
		return nil, sentinel.ErrNilClient
	}
	// Check if the `capacity` is valid
	if rb.capacity < 0 {
		return nil, sentinel.ErrInvalidCapacity
	}
	// Check if the `keysSetName` is empty
	if rb.keysSetName == "" {
		rb.keysSetName = constants.RedisBackend
	}

	// Check if the serializer is nil
	if rb.Serializer == nil {
		var err error
		// Set a the serializer to default to `msgpack`
		rb.Serializer, err = serializer.New("msgpack")
		if err != nil {
			return nil, err
		}
	}

	// return the new backend
	return rb, nil
}

// SetCapacity sets the capacity of the cache.
func (cacheBackend *Redis) SetCapacity(capacity int) {
	if capacity < 0 {
		return
	}

	cacheBackend.capacity = capacity
}

// Capacity returns the maximum number of items that can be stored in the cache.
func (cacheBackend *Redis) Capacity() int {
	return cacheBackend.capacity
}

// Count returns the number of items in the cache.
func (cacheBackend *Redis) Count(ctx context.Context) int {
	count, err := cacheBackend.rdb.DBSize(ctx).Result()
	if err != nil {
		return 0
	}

	return int(count)
}

// Get retrieves the Item with the given key from the cacheBackend. If the item is not found, it returns nil.
func (cacheBackend *Redis) Get(ctx context.Context, key string) (*cache.Item, bool) {
	// Check if the key is in the set of keys
	isMember, err := cacheBackend.rdb.SIsMember(ctx, cacheBackend.keysSetName, key).Result()
	if err != nil {
		return nil, false
	}

	if !isMember {
		return nil, false
	}
	// Get a transient item from pool, but clone before returning to caller
	pooled := cacheBackend.itemPoolManager.Get()

	data, err := cacheBackend.rdb.HGet(ctx, key, "data").Bytes()
	if err != nil {
		// Check if the item is not found
		if errors.Is(err, redis.Nil) {
			return nil, false
		}

		return nil, false
	}
	// Deserialize the item
	err = cacheBackend.Serializer.Unmarshal(data, pooled)
	if err != nil {
		return nil, false
	}
	// Clone into a new heap object to avoid returning a pooled pointer
	out := *pooled
	cacheBackend.itemPoolManager.Put(pooled)

	return &out, true
}

// Set stores the Item in the cacheBackend.
func (cacheBackend *Redis) Set(ctx context.Context, item *cache.Item) error {
	return redisSet(ctx, cacheBackend.rdb, cacheBackend.keysSetName, item, cacheBackend.Serializer)
}

// List returns a list of all the items in the cacheBackend that match the given filter options.
func (cacheBackend *Redis) List(ctx context.Context, filters ...IFilter) ([]*cache.Item, error) {
	return redisList(ctx, cacheBackend.rdb, cacheBackend.keysSetName, cacheBackend.Serializer, cacheBackend.itemPoolManager, filters...)
}

// Remove removes an item from the cache with the given key.
func (cacheBackend *Redis) Remove(ctx context.Context, keys ...string) error {
	return redisRemove(ctx, cacheBackend.rdb, cacheBackend.keysSetName, keys...)
}

// Clear removes all items from the cache.
func (cacheBackend *Redis) Clear(ctx context.Context) error {
	_, err := cacheBackend.rdb.FlushDB(ctx).Result()

	return ewrap.Wrap(err, "flushing database", ewrap.WithRetry(maxRetries, retriesDelay))
}
