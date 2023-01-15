package backend

import (
	"sort"
	"time"

	"github.com/go-redis/redis"
	"github.com/hyp3rd/hypercache/errors"
	"github.com/hyp3rd/hypercache/models"
	"github.com/hyp3rd/hypercache/types"
)

// RedisBackend is a cache backend that stores the items in a redis implementation.
type RedisBackend struct {
	client      *redis.Client // redis client to interact with the redis server
	capacity    int           // capacity of the cache, limits the number of items that can be stored in the cache
	SortFilters               // SortFilters holds the filters applied when listing the items in the cache
}

// NewRedisBackend creates a new redis cache with the given options.
func NewRedisBackend[T RedisBackend](redisOptions ...BackendOption[RedisBackend]) (backend IRedisBackend[T], err error) {
	rb := &RedisBackend{}
	// Apply the backend options
	ApplyBackendOptions(rb, redisOptions...)

	// Check if the client is nil
	if rb.client == nil {
		return nil, errors.ErrNilClient
	}
	// Check if the capacity is valid
	if rb.capacity < 0 {
		return nil, errors.ErrInvalidCapacity
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
	count, _ := cacheBackend.client.DBSize().Result()
	return int(count)
}

// Size returns the number of items in the cache.
func (cacheBackend *RedisBackend) Size() int {
	return cacheBackend.itemCount()
}

// Get retrieves the Item with the given key from the cacheBackend. If the item is not found, it returns nil.
func (cacheBackend *RedisBackend) Get(key string) (item *models.Item, ok bool) {
	data, err := cacheBackend.client.HGetAll(key).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, false
		}
		return nil, false
	}

	item = models.ItemPool.Get().(*models.Item)
	item.UnmarshalBinary([]byte(data["data"]))
	item.Expiration, _ = time.ParseDuration(data["expiration"])

	return item, true
}

// Set stores the Item in the cacheBackend.
func (cacheBackend *RedisBackend) Set(item *models.Item) error {
	if err := item.Valid(); err != nil {
		return err
	}

	data, _ := item.MarshalBinary()
	expiration := item.Expiration.String()

	cacheBackend.client.HMSet(item.Key, map[string]interface{}{
		"data":       data,
		"expiration": expiration,
	})

	if item.Expiration >= 0 {
		cacheBackend.client.Expire(item.Key, item.Expiration)
	}

	return nil
}

// List returns a list of all the items in the cacheBackend that match the given filter options.
func (cacheBackend *RedisBackend) List(options ...FilterOption[RedisBackend]) ([]*models.Item, error) {
	// Apply the filter options
	ApplyFilterOptions(cacheBackend, options...)

	// Get all keys
	keys, _ := cacheBackend.client.HKeys("*").Result()

	items := make([]*models.Item, 0, len(keys))

	for _, key := range keys {
		item, ok := cacheBackend.Get(key)
		if !ok {
			continue
		}
		if cacheBackend.FilterFunc != nil && !cacheBackend.FilterFunc(item) {
			continue
		}
		items = append(items, item)
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

// func (cacheBackend *RedisBackend) List(options ...FilterOption[RedisBackend]) ([]*models.Item, error) {
// 	// Apply the filter options
// 	ApplyFilterOptions(cacheBackend, options...)

// 	// Get all keys
// 	keys, _ := cacheBackend.client.Keys("*").Result()

// 	items := make([]*models.Item, 0, len(keys))

// 	for _, key := range keys {
// 		item, ok := cacheBackend.Get(key)
// 		if !ok {
// 			continue
// 		}
// 		if cacheBackend.FilterFunc != nil && !cacheBackend.FilterFunc(item) {
// 			continue
// 		}
// 		items = append(items, item)
// 	}

// 	// Sort items
// 	if cacheBackend.SortBy == "" {
// 		return items, nil
// 	}

// 	sort.Slice(items, func(i, j int) bool {
// 		a := items[i].FieldByName(cacheBackend.SortBy)
// 		b := items[j].FieldByName(cacheBackend.SortBy)
// 		switch cacheBackend.SortBy {
// 		case types.SortByKey.String():
// 			if cacheBackend.SortAscending {
// 				return a.Interface().(string) < b.Interface().(string)
// 			}
// 			return a.Interface().(string) > b.Interface().(string)
// 		case types.SortByValue.String():
// 			if cacheBackend.SortAscending {
// 				return a.Interface().(string) < b.Interface().(string)
// 			}
// 			return a.Interface().(string) > b.Interface().(string)
// 		case types.SortByLastAccess.String():
// 			if cacheBackend.SortAscending {
// 				return a.Interface().(time.Time).Before(b.Interface().(time.Time))
// 			}
// 			return a.Interface().(time.Time).After(b.Interface().(time.Time))
// 		case types.SortByAccessCount.String():
// 			if cacheBackend.SortAscending {
// 				return a.Interface().(uint) < b.Interface().(uint)
// 			}
// 			return a.Interface().(uint) > b.Interface().(uint)
// 		case types.SortByExpiration.String():
// 			if cacheBackend.SortAscending {
// 				return a.Interface().(time.Duration) < b.Interface().(time.Duration)
// 			}
// 			return a.Interface().(time.Duration) > b.Interface().(time.Duration)
// 		default:
// 			return false
// 		}
// 	})

// 	return items, nil
// }

// Remove removes an item from the cache with the given key
func (cacheBackend *RedisBackend) Remove(keys ...string) error {
	_, err := cacheBackend.client.Del(keys...).Result()
	return err
}

// Clear removes all items from the cache
func (cacheBackend *RedisBackend) Clear() error {
	_, err := cacheBackend.client.FlushDB().Result()
	return err
}
