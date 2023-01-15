package backend

import (
	"context"
	"sort"
	"time"

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
	prefix      string                 // prefix is the prefix used to prefix the keys of the items in the cache
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

	if rb.prefix == "" {
		rb.prefix = "hypercache-"
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
	// Get the item from the cacheBackend
	item = models.ItemPool.Get().(*models.Item)
	// fmt.Println(fmt.Sprintf("%s%s", cacheBackend.prefix, key))
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

// generateKey
// @param key
// func generateKey(key string) string {
// 	hash := fnv.New64a()
// 	_, _ = hash.Write([]byte(key))

// 	return strconv.FormatUint(hash.Sum64(), 36)
// }

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

	// Set the item in the cacheBackend
	// fmt.Println(fmt.Sprintf("%s%s", cacheBackend.prefix, item.Key))
	err = cacheBackend.rdb.HSet(context.Background(), item.Key, map[string]interface{}{
		"data":       data,
		"expiration": expiration,
	}).Err()

	if err != nil {
		return err
	}

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

	var (
		cursor uint64   // cursor used to iterate over the keys
		keys   []string // keys of the items in the cacheBackend
		err    error    // error returned by the redis client
	)

	// Iterate over the keys in the cacheBackend
	for {
		var nextCursor uint64 // next cursor returned by the redis client
		// Scan the keys in the cacheBackend
		// fmt.Println(fmt.Sprintf("%s*", cacheBackend.prefix))
		keys, nextCursor, err = cacheBackend.rdb.Scan(context.Background(), cursor, "*", 100).Result()
		if err != nil {
			return nil, err
		}
		// Update the cursor
		cursor = nextCursor
		if cursor == 0 {
			// No more keys to iterate over
			break
		}
	}

	// Create a slice to hold the items
	items := make([]*models.Item, 0, len(keys))

	// Iterate over the keys and retrieve the items
	for _, key := range keys {
		// Get the item from the cacheBackend
		// fmt.Println("gettimg key", key)
		item, ok := cacheBackend.Get(key)
		if !ok {
			continue
		}
		// Check if the item matches the filter options
		if cacheBackend.FilterFunc != nil && !cacheBackend.FilterFunc(item) {
			continue
		}
		items = append(items, item)
	}

	// Sort the items
	if cacheBackend.SortBy == "" {
		// No sorting
		return items, nil
	}

	sort.Slice(items, func(i, j int) bool {
		a := items[i].FieldByName(cacheBackend.SortBy)
		b := items[j].FieldByName(cacheBackend.SortBy)
		switch cacheBackend.SortBy {
		case types.SortByKey.String():
			if cacheBackend.SortAscending {
				return a.Interface().(string) < b.Interface().(string)
			}
			return a.Interface().(string) > b.Interface().(string)
		case types.SortByValue.String():
			if cacheBackend.SortAscending {
				return a.Interface().(string) < b.Interface().(string)
			}
			return a.Interface().(string) > b.Interface().(string)
		case types.SortByLastAccess.String():
			if cacheBackend.SortAscending {
				return a.Interface().(time.Time).Before(b.Interface().(time.Time))
			}
			return a.Interface().(time.Time).After(b.Interface().(time.Time))
		case types.SortByAccessCount.String():
			if cacheBackend.SortAscending {
				return a.Interface().(uint) < b.Interface().(uint)
			}
			return a.Interface().(uint) > b.Interface().(uint)
		case types.SortByExpiration.String():
			if cacheBackend.SortAscending {
				return a.Interface().(time.Duration) < b.Interface().(time.Duration)
			}
			return a.Interface().(time.Duration) > b.Interface().(time.Duration)
		default:
			return false
		}
	})

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
