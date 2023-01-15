package backend

import (
	"context"
	"fmt"
	"sort"

	"github.com/go-redis/redis/v8"
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

	rb.Serializer = serializer.New()

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
	// Check if the key is in the set of keys
	isMember, err := cacheBackend.rdb.SIsMember(context.Background(), cacheBackend.keysSetName, key).Result()
	if err != nil {
		return nil, false
	}
	if !isMember {
		return nil, false
	}

	// Get the item from the cacheBackend
	item = models.ItemPool.Get().(*models.Item)
	data, err := cacheBackend.rdb.HGet(context.Background(), key, "data").Bytes()
	if err != nil {
		// Check if the item is not found
		if err == redis.Nil {
			return nil, false
		}
		return nil, false
	}
	// Deserialize the item
	err = cacheBackend.Serializer.Deserialize(data, item)
	if err != nil {
		return nil, false
	}
	return item, true
}

// Set stores the Item in the cacheBackend.
func (cacheBackend *RedisBackend) Set(item *models.Item) error {
	// Check if the item is valid
	if err := item.Valid(); err != nil {
		return err
	}

	// Serialize the item
	data, err := cacheBackend.Serializer.Serialize(item)
	if err != nil {
		return err
	}

	expiration := item.Expiration.String()

	// Store the item in the cacheBackend
	err = cacheBackend.rdb.HSet(context.Background(), item.Key, map[string]interface{}{
		"data":       data,
		"expiration": expiration,
	}).Err()

	if err != nil {
		return err
	}

	// Add the key to the set of keys associated with the cache prefix
	cacheBackend.rdb.SAdd(context.Background(), cacheBackend.keysSetName, item.Key)

	// Set the expiration if it is not zero
	if item.Expiration > 0 {
		cacheBackend.rdb.Expire(context.Background(), item.Key, item.Expiration)
	}

	return nil
}

// List returns a list of all the items in the cacheBackend that match the given filter options.
func (cacheBackend *RedisBackend) List(options ...FilterOption[RedisBackend]) ([]*models.Item, error) {
	// Apply the filter options
	ApplyFilterOptions(cacheBackend, options...)

	// Get the set of keys
	keys, err := cacheBackend.rdb.SMembers(context.Background(), cacheBackend.keysSetName).Result()
	if err != nil {
		return nil, err
	}

	// Create a slice to hold the items
	items := make([]*models.Item, 0, len(keys))

	// Iterate over the keys and retrieve the items
	for _, key := range keys {
		// Get the item from the cacheBackend
		item, ok := cacheBackend.Get(key)
		if ok {
			// Check if the item matches the filter options
			if cacheBackend.FilterFunc != nil && !cacheBackend.FilterFunc(item) {
				continue
			}
			items = append(items, item)
		}
	}

	// Sort the items
	if cacheBackend.SortBy == "" {
		// No sorting
		return items, nil
	}

	var sorter sort.Interface
	switch cacheBackend.SortBy {
	case types.SortByKey.String():
		sorter = &itemSorterByKey{items: items}
	case types.SortByValue.String():
		sorter = &itemSorterByValue{items: items}
	case types.SortByLastAccess.String():
		sorter = &itemSorterByLastAccess{items: items}
	case types.SortByAccessCount.String():
		sorter = &itemSorterByAccessCount{items: items}
	case types.SortByExpiration.String():
		sorter = &itemSorterByExpiration{items: items}
	default:
		return nil, fmt.Errorf("unknown sortBy field: %s", cacheBackend.SortBy)
	}

	if !cacheBackend.SortAscending {
		sorter = sort.Reverse(sorter)
	}

	sort.Sort(sorter)

	return items, nil
}

// func (cacheBackend *RedisBackend) List(options ...FilterOption[RedisBackend]) ([]*models.Item, error) {
// 	// Apply the filter options
// 	// Apply the filter options
// 	ApplyFilterOptions(cacheBackend, options...)

// 	// Initialize the score range
// 	var min, max string
// 	if cacheBackend.SortAscending {
// 		min = "-inf"
// 		max = "+inf"
// 	} else {
// 		min = "+inf"
// 		max = "-inf"
// 	}

// 	// Get the keys from the sorted set
// 	var keys []string
// 	var err error
// 	zrangeby := &redis.ZRangeBy{
// 		Min: min, Max: max,
// 	}
// 	if cacheBackend.SortBy == types.SortByLastAccess.String() {
// 		keys, err = cacheBackend.rdb.ZRangeByScore(context.Background(), "last_access_sort", zrangeby).Result()
// 	} else if cacheBackend.SortBy == types.SortByAccessCount.String() {
// 		keys, err = cacheBackend.rdb.ZRangeByScore(context.Background(), "access_count_sort", zrangeby).Result()
// 	} else {
// 		return nil, fmt.Errorf("invalid sorting criteria: %s", cacheBackend.SortBy)
// 	}
// 	if err != nil {
// 		return nil, err
// 	}

// 	// Create a slice to hold the items
// 	items := make([]*models.Item, 0, len(keys))

// 	// Iterate over the keys and retrieve the items
// 	for _, key := range keys {
// 		// Get the item from the cacheBackend
// 		item, ok := cacheBackend.Get(key)
// 		if !ok {
// 			continue
// 		}
// 		// Check if the item matches the filter options
// 		if cacheBackend.FilterFunc != nil && !cacheBackend.FilterFunc(item) {
// 			continue
// 		}
// 		items = append(items, item)
// 	}

// 	return items, nil
// }

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
