package backend

import (
	"sort"
	"time"

	"github.com/go-redis/redis"
	"github.com/hyp3rd/hypercache/cache"
	"github.com/hyp3rd/hypercache/errors"
	"github.com/hyp3rd/hypercache/types"
)

type RedisBackend struct {
	client   *redis.Client // redis client to interact with the redis server
	capacity int           // capacity of the cache, limits the number of items that can be stored in the cache
	// sortBy is the field to sort the items by.
	// The field can be any of the fields in the `CacheItem` struct.
	sortBy string
	// sortAscending is a boolean indicating whether the items should be sorted in ascending order.
	// If set to false, the items will be sorted in descending order.
	sortAscending bool
	// filterFunc is a predicate that takes a `CacheItem` as an argument and returns a boolean indicating whether the item should be included in the cache.
	filterFunc func(item *cache.CacheItem) bool // filters applied when listing the items in the cache
}

func NewRedisBackend[T RedisBackend](client *redis.Client, capacity int) (backend IRedisBackend[T], err error) {
	if client == nil {
		return nil, errors.ErrNilClient
	}
	if capacity < 0 {
		return nil, errors.ErrInvalidCapacity
	}

	return &RedisBackend{
		client:   client,
		capacity: capacity,
	}, nil
}

// Capacity returns the maximum number of items that can be stored in the cache.
func (cacheBackend *RedisBackend) Capacity() int {
	return cacheBackend.capacity
}

func (cacheBackend *RedisBackend) SetCapacity(capacity int) {
	if capacity < 0 {
		return
	}

	cacheBackend.capacity = capacity
}

func (cacheBackend *RedisBackend) itemCount() int {
	count, _ := cacheBackend.client.DBSize().Result()
	return int(count)
}

func (cacheBackend *RedisBackend) Size() int {
	return cacheBackend.itemCount()
}

func (cacheBackend *RedisBackend) Get(key string) (item *cache.CacheItem, ok bool) {
	data, err := cacheBackend.client.Get(key).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, false
		}
		return nil, false
	}

	item = cache.CacheItemPool.Get().(*cache.CacheItem)
	item.UnmarshalBinary([]byte(data))
	return item, true
}

func (cacheBackend *RedisBackend) Set(item *cache.CacheItem) error {
	if err := item.Valid(); err != nil {
		return err
	}

	data, _ := item.MarshalBinary()
	if item.Expiration > 0 {
		cacheBackend.client.Set(item.Key, data, item.Expiration)
	} else {
		cacheBackend.client.Set(item.Key, data, 0)
	}

	return nil
}

func (cacheBackend *RedisBackend) List(options ...FilterOption[RedisBackend]) ([]*cache.CacheItem, error) {
	// Apply the filter options
	ApplyBackendOptions(cacheBackend, options...)

	// Get all keys
	keys, _ := cacheBackend.client.Keys("*").Result()

	items := make([]*cache.CacheItem, 0, len(keys))

	for _, key := range keys {
		item, ok := cacheBackend.Get(key)
		if !ok {
			continue
		}
		if cacheBackend.filterFunc != nil && !cacheBackend.filterFunc(item) {
			continue
		}
		items = append(items, item)
	}

	// Sort items
	if cacheBackend.sortBy == "" {
		return items, nil
	}

	sort.Slice(items, func(i, j int) bool {
		a := items[i].FieldByName(cacheBackend.sortBy)
		b := items[j].FieldByName(cacheBackend.sortBy)
		switch cacheBackend.sortBy {
		case types.SortByKey.String():
			if cacheBackend.sortAscending {
				return a.Interface().(string) < b.Interface().(string)
			}
			return a.Interface().(string) > b.Interface().(string)
		case types.SortByValue.String():
			if cacheBackend.sortAscending {
				return a.Interface().(string) < b.Interface().(string)
			}
			return a.Interface().(string) > b.Interface().(string)
		case types.SortByLastAccess.String():
			if cacheBackend.sortAscending {
				return a.Interface().(time.Time).Before(b.Interface().(time.Time))
			}
			return a.Interface().(time.Time).After(b.Interface().(time.Time))
		case types.SortByAccessCount.String():
			if cacheBackend.sortAscending {
				return a.Interface().(uint) < b.Interface().(uint)
			}
			return a.Interface().(uint) > b.Interface().(uint)
		case types.SortByExpiration.String():
			if cacheBackend.sortAscending {
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
	_, err := cacheBackend.client.Del(keys...).Result()
	return err
}

// Clear removes all items from the cache
func (cacheBackend *RedisBackend) Clear() error {
	_, err := cacheBackend.client.FlushDB().Result()
	return err
}
