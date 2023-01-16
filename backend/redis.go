package backend

import (
	"context"
	"fmt"
	"sort"

	"github.com/go-redis/redis/v9"
	"github.com/hyp3rd/hypercache/errors"
	"github.com/hyp3rd/hypercache/libs/serializer"
	"github.com/hyp3rd/hypercache/models"
	"github.com/hyp3rd/hypercache/types"
)

// RedisBackend is a cache backend that stores the items in a redis implementation.
type RedisBackend struct {
	rdb         *redis.Client          // redis client to interact with the redis server
	capacity    int                    // capacity of the cache, limits the number of items that can be stored in the cache
	keysSetName string                 // keysSetName is the name of the set that holds the keys of the items in the cache
	Serializer  serializer.ISerializer // Serializer is the serializer used to serialize the items before storing them in the cache
	SortFilters                        // SortFilters holds the filters applied when listing the items in the cache
}

// NewRedisBackend creates a new redis cache with the given options.
func NewRedisBackend[T RedisBackend](redisOptions ...Option[RedisBackend]) (backend IRedisBackend[T], err error) {
	rb := &RedisBackend{}
	// Apply the backend options
	ApplyOptions(rb, redisOptions...)

	// Check if the client is nil
	if rb.rdb == nil {
		return nil, errors.ErrNilClient
	}
	// Check if the capacity is valid
	if rb.capacity < 0 {
		return nil, errors.ErrInvalidCapacity
	}

	if rb.keysSetName == "" {
		rb.keysSetName = "hypercache"
	}

	rb.Serializer, err = serializer.New("msgpack")
	if err != nil {
		return nil, err
	}

	// return the new backend
	return rb, nil
}

// Capacity returns the maximum number of items that can be stored in the cache.
func (cacheBackend *RedisBackend) Capacity() int {
	return cacheBackend.capacity
}

// SetCapacity sets the capacity of the cache.
func (cacheBackend *RedisBackend) SetCapacity(capacity int) {
	if capacity < 0 {
		return
	}
	cacheBackend.capacity = capacity
}

// itemCount returns the number of items in the cache.
func (cacheBackend *RedisBackend) itemCount() int {
	count, _ := cacheBackend.rdb.DBSize(context.Background()).Result()
	return int(count)
}

// Size returns the number of items in the cache.
func (cacheBackend *RedisBackend) Size() int {
	return cacheBackend.itemCount()
}

// Get retrieves the Item with the given key from the cacheBackend. If the item is not found, it returns nil.
func (cacheBackend *RedisBackend) Get(key string) (item *models.Item, ok bool) {
	pipe := cacheBackend.rdb.TxPipeline()
	// Check if the key is in the set of keys
	// isMember, err := cacheBackend.rdb.SIsMember(context.Background(), cacheBackend.keysSetName, key).Result()
	isMember, err := pipe.SIsMember(context.Background(), cacheBackend.keysSetName, key).Result()
	if err != nil {
		return nil, false
	}
	if !isMember {
		return nil, false
	}

	// Get the item from the cacheBackend
	item = models.ItemPool.Get().(*models.Item)
	// data, err := cacheBackend.rdb.HGet(context.Background(), key, "data").Bytes()
	data, err := pipe.HGet(context.Background(), key, "data").Bytes()
	pipe.Exec(context.Background())
	if err != nil {
		// Return the item to the pool
		models.ItemPool.Put(item)

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
func (cacheBackend *RedisBackend) Set(item *models.Item) error {
	pipe := cacheBackend.rdb.TxPipeline()

	// Check if the item is valid
	if err := item.Valid(); err != nil {
		// Return the item to the pool
		models.ItemPool.Put(item)
		return err
	}

	// Serialize the item
	data, err := cacheBackend.Serializer.Marshal(item)
	if err != nil {
		// Return the item to the pool
		models.ItemPool.Put(item)
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
func (cacheBackend *RedisBackend) List(options ...FilterOption[RedisBackend]) ([]*models.Item, error) {
	// Apply the filter options
	ApplyFilterOptions(cacheBackend, options...)

	// Get the set of keys held in the cacheBackend with the given `keysSetName`
	keys, err := cacheBackend.rdb.SMembers(context.Background(), cacheBackend.keysSetName).Result()
	if err != nil {
		return nil, err
	}

	// Execute the Redis commands in a pipeline transaction to improve performance and reduce the number of round trips
	cmds, err := cacheBackend.rdb.Pipelined(context.Background(), func(pipe redis.Pipeliner) error {
		// Get the items from the cacheBackend
		for _, key := range keys {
			pipe.HGet(context.Background(), key, "data")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Create a slice to hold the items
	items := make([]*models.Item, 0, len(keys))

	// Deserialize the items and add them to the slice of items to return
	for _, cmd := range cmds {
		data, _ := cmd.(*redis.StringCmd).Bytes() // Ignore the error because it is already checked in the pipeline transaction
		item := models.ItemPool.Get().(*models.Item)
		err := cacheBackend.Serializer.Unmarshal(data, item)
		if err == nil {
			if cacheBackend.FilterFunc != nil && !cacheBackend.FilterFunc(item) {
				continue
			}
			items = append(items, item)
		} else {
			// Return the item to the pool
			models.ItemPool.Put(item)
		}
	}

	// Check if the items should be sorted
	if cacheBackend.SortBy == "" {
		// No sorting
		return items, nil
	}

	// Sort the items
	var sorter sort.Interface
	switch cacheBackend.SortBy {
	case types.SortByKey.String(): // Sort by key
		sorter = &itemSorterByKey{items: items}
	case types.SortByValue.String(): // Sort by value: it can cause problems if the value is not a string
		sorter = &itemSorterByValue{items: items}
	case types.SortByLastAccess.String(): // Sort by last access
		sorter = &itemSorterByLastAccess{items: items}
	case types.SortByAccessCount.String(): // Sort by access count
		sorter = &itemSorterByAccessCount{items: items}
	case types.SortByExpiration.String(): // Sort by expiration
		sorter = &itemSorterByExpiration{items: items}
	default:
		return nil, fmt.Errorf("unknown sortBy field: %s", cacheBackend.SortBy)
	}

	// Reverse the sort order if needed
	if !cacheBackend.SortAscending {
		sorter = sort.Reverse(sorter)
	}
	// Sort the items by the given field
	sort.Sort(sorter)

	return items, nil
}

// Remove removes an item from the cache with the given key
func (cacheBackend *RedisBackend) Remove(keys ...string) error {
	_, err := cacheBackend.rdb.Del(context.Background(), keys...).Result()
	return err
}

// Clear removes all items from the cache
func (cacheBackend *RedisBackend) Clear() error {
	_, err := cacheBackend.rdb.FlushDB(context.Background()).Result()
	return err
}
