package backend

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/hyp3rd/ewrap"

	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/internal/sentinel"
	"github.com/hyp3rd/hypercache/libs/serializer"
	"github.com/hyp3rd/hypercache/types"
)

const (
	maxRetries   = 3
	retriesDelay = 100 * time.Millisecond
)

// Redis is a cache backend that stores the items in a redis implementation.
type Redis struct {
	rdb             *redis.Client          // redis client to interact with the redis server
	capacity        int                    // capacity of the cache, limits the number of items that can be stored in the cache
	itemPoolManager *types.ItemPoolManager // itemPoolManager is used to manage the item pool for memory efficiency
	keysSetName     string                 // keysSetName is the name of the set that holds the keys of the items in the cache
	Serializer      serializer.ISerializer // Serializer is the serializer used to serialize the items before storing them in the cache
}

// NewRedis creates a new redis cache with the given options.
func NewRedis(redisOptions ...Option[Redis]) (IBackend[Redis], error) {
	rb := &Redis{
		itemPoolManager: types.NewItemPoolManager(),
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
func (cacheBackend *Redis) Get(ctx context.Context, key string) (*types.Item, bool) {
	// Check if the key is in the set of keys
	isMember, err := cacheBackend.rdb.SIsMember(ctx, cacheBackend.keysSetName, key).Result()
	if err != nil {
		return nil, false
	}

	if !isMember {
		return nil, false
	}
	// Get the item from the cacheBackend
	item := cacheBackend.itemPoolManager.Get()

	// Return the item to the pool
	defer cacheBackend.itemPoolManager.Put(item)

	data, err := cacheBackend.rdb.HGet(ctx, key, "data").Bytes()
	if err != nil {
		// Check if the item is not found
		if errors.Is(err, redis.Nil) {
			return nil, false
		}

		return nil, false
	}
	// Deserialize the item
	err = cacheBackend.Serializer.Unmarshal(data, item)
	if err != nil {
		return nil, false
	}

	return item, true
}

// Set stores the Item in the cacheBackend.
func (cacheBackend *Redis) Set(ctx context.Context, item *types.Item) error {
	pipe := cacheBackend.rdb.TxPipeline()

	// Check if the item is valid
	err := item.Valid()
	if err != nil {
		// Return the item to the pool
		return err
	}

	// Serialize the item
	data, err := cacheBackend.Serializer.Marshal(item)
	if err != nil {
		// Return the item to the pool
		return err
	}

	expiration := item.Expiration.String()

	// Store the item in the cacheBackend
	err = pipe.HSet(ctx, item.Key, map[string]any{
		"data":       data,
		"expiration": expiration,
	}).Err()
	if err != nil {
		return ewrap.Wrap(err, "failed to set item in redis")
	}
	// Add the key to the set of keys associated with the cache prefix
	pipe.SAdd(ctx, cacheBackend.keysSetName, item.Key)
	// Set the expiration if it is not zero
	if item.Expiration > 0 {
		pipe.Expire(ctx, item.Key, item.Expiration)
	}

	_, err = pipe.Exec(ctx)
	if err != nil {
		return ewrap.Wrap(err, "failed to execute redis pipeline")
	}

	return nil
}

// List returns a list of all the items in the cacheBackend that match the given filter options.
func (cacheBackend *Redis) List(ctx context.Context, filters ...IFilter) ([]*types.Item, error) {
	// Get the set of keys held in the cacheBackend with the given `keysSetName`
	keys, err := cacheBackend.rdb.SMembers(ctx, cacheBackend.keysSetName).Result()
	if err != nil {
		return nil, ewrap.Wrap(err, "failed to get keys from redis")
	}

	// Execute the Redis commands in a pipeline transaction to improve performance and reduce the number of round trips
	cmds, err := cacheBackend.rdb.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		// Get the items from the cacheBackend
		for _, key := range keys {
			pipe.HGetAll(ctx, key)
		}

		return nil
	})
	if err != nil {
		return nil, ewrap.Wrap(err, "failed to execute redis pipeline while listing")
	}

	// Create a slice to hold the items
	items := make([]*types.Item, 0, len(keys))

	// Deserialize the items and add them to the slice of items to return
	for _, cmd := range cmds {
		command, ok := cmd.(*redis.MapStringStringCmd)
		if !ok {
			continue
		}

		data, err := command.Result() // Change the type assertion to match HGetAll
		if err != nil {
			return nil, ewrap.Wrap(err, "failed to get item data from redis")
		}

		item := cacheBackend.itemPoolManager.Get()
		// Return the item to the pool
		defer cacheBackend.itemPoolManager.Put(item)

		err = cacheBackend.Serializer.Unmarshal([]byte(data["data"]), item)
		if err == nil {
			items = append(items, item)
		}
	}

	// Apply the filters
	if len(filters) > 0 {
		for _, filter := range filters {
			items, err = filter.ApplyFilter(constants.RedisBackend, items)
		}
	}

	return items, err
}

// Remove removes an item from the cache with the given key.
func (cacheBackend *Redis) Remove(ctx context.Context, keys ...string) error {
	pipe := cacheBackend.rdb.TxPipeline()

	_, err := pipe.SRem(ctx, cacheBackend.keysSetName, keys).Result()
	if err != nil {
		return ewrap.Wrap(err, "removing keys from set")
	}

	_, err = pipe.Del(ctx, keys...).Result()
	if err != nil {
		return ewrap.Wrap(err, "removing keys")
	}

	_, err = pipe.Exec(ctx)

	return ewrap.Wrap(err, "executing pipeline")
}

// Clear removes all items from the cache.
func (cacheBackend *Redis) Clear(ctx context.Context) error {
	_, err := cacheBackend.rdb.FlushDB(ctx).Result()

	return ewrap.Wrap(err, "flushing database", ewrap.WithRetry(maxRetries, retriesDelay))
}
