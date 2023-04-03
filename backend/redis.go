package backend

import (
	"context"
	"sync"

	"github.com/hyp3rd/hypercache/errors"
	"github.com/hyp3rd/hypercache/libs/serializer"
	"github.com/hyp3rd/hypercache/types"
	"github.com/redis/go-redis/v9"
)

// Redis is a cache backend that stores the items in a redis implementation.
type Redis struct {
	rdb         *redis.Client          // redis client to interact with the redis server
	capacity    int                    // capacity of the cache, limits the number of items that can be stored in the cache
	keysSetName string                 // keysSetName is the name of the set that holds the keys of the items in the cache
	Serializer  serializer.ISerializer // Serializer is the serializer used to serialize the items before storing them in the cache
	// SortFilters                        // SortFilters holds the filters applied when listing the items in the cache
}

// NewRedis creates a new redis cache with the given options.
func NewRedis(redisOptions ...Option[Redis]) (backend IBackend[Redis], err error) {
	rb := &Redis{}
	// Apply the backend options
	ApplyOptions(rb, redisOptions...)

	// Check if the client is nil
	if rb.rdb == nil {
		return nil, errors.ErrNilClient
	}
	// Check if the `capacity` is valid
	if rb.capacity < 0 {
		return nil, errors.ErrInvalidCapacity
	}
	// Check if the `keysSetName` is empty
	if rb.keysSetName == "" {
		rb.keysSetName = "hypercache"
	}

	// Check if the serializer is nil
	if rb.Serializer == nil {
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
func (cacheBackend *Redis) Count() int {
	count, _ := cacheBackend.rdb.DBSize(context.Background()).Result()
	return int(count)
}

// Get retrieves the Item with the given key from the cacheBackend. If the item is not found, it returns nil.
func (cacheBackend *Redis) Get(key string) (item *types.Item, ok bool) {
	// Check if the key is in the set of keys
	isMember, err := cacheBackend.rdb.SIsMember(context.Background(), cacheBackend.keysSetName, key).Result()
	if err != nil {
		return nil, false
	}
	if !isMember {
		return nil, false
	}

	// Get the item from the cacheBackend
	item = types.ItemPool.Get().(*types.Item)
	// Return the item to the pool
	defer types.ItemPool.Put(item)

	data, err := cacheBackend.rdb.HGet(context.Background(), key, "data").Bytes()
	if err != nil {
		// Check if the item is not found
		if err == redis.Nil {
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
func (cacheBackend *Redis) Set(item *types.Item) error {
	pipe := cacheBackend.rdb.TxPipeline()

	// Check if the item is valid
	if err := item.Valid(); err != nil {
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
	err = pipe.HSet(context.Background(), item.Key, map[string]interface{}{
		"data":       data,
		"expiration": expiration,
	}).Err()

	if err != nil {
		return err
	}
	// Add the key to the set of keys associated with the cache prefix
	pipe.SAdd(context.Background(), cacheBackend.keysSetName, item.Key)
	// Set the expiration if it is not zero
	if item.Expiration > 0 {
		pipe.Expire(context.Background(), item.Key, item.Expiration)
	}

	pipe.Exec(context.Background())

	return nil
}

// List returns a list of all the items in the cacheBackend that match the given filter options.
func (cacheBackend *Redis) List(ctx context.Context, filters ...IFilter) ([]*types.Item, error) {
	// Get the set of keys held in the cacheBackend with the given `keysSetName`
	keys, err := cacheBackend.rdb.SMembers(ctx, cacheBackend.keysSetName).Result()
	if err != nil {
		return nil, err
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
		return nil, err
	}

	// Create a slice to hold the items
	items := make([]*types.Item, 0, len(keys))

	// Deserialize the items and add them to the slice of items to return
	for _, cmd := range cmds {
		data, _ := cmd.(*redis.MapStringStringCmd).Result() // Change the type assertion to match HGetAll
		item := types.ItemPool.Get().(*types.Item)
		// Return the item to the pool
		defer types.ItemPool.Put(item)
		err := cacheBackend.Serializer.Unmarshal([]byte(data["data"]), item)
		if err == nil {
			items = append(items, item)
		}
	}

	// Apply the filters
	if len(filters) > 0 {
		wg := sync.WaitGroup{}
		wg.Add(len(filters))
		for _, filter := range filters {
			go func(filter IFilter) {
				defer wg.Done()
				items = filter.ApplyFilter("redis", items)
			}(filter)
		}
		wg.Wait()
	}

	return items, nil
}

// Remove removes an item from the cache with the given key
func (cacheBackend *Redis) Remove(ctx context.Context, keys ...string) error {
	pipe := cacheBackend.rdb.TxPipeline()

	pipe.SRem(ctx, cacheBackend.keysSetName, keys).Result()
	pipe.Del(ctx, keys...).Result()

	_, err := pipe.Exec(ctx)
	return err
}

// Clear removes all items from the cache
func (cacheBackend *Redis) Clear(ctx context.Context) error {
	_, err := cacheBackend.rdb.FlushDB(ctx).Result()
	return err
}
