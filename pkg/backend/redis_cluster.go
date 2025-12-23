package backend

import (
	"context"
	"errors"
	"time"

	"github.com/hyp3rd/ewrap"
	"github.com/redis/go-redis/v9"

	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/internal/libs/serializer"
	"github.com/hyp3rd/hypercache/internal/sentinel"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

const (
	clusterMaxRetries   = 3
	clusterRetriesDelay = 100 * time.Millisecond
)

// RedisCluster is a cache backend that stores items in a Redis Cluster.
// It mirrors the single-node Redis backend semantics but uses go-redis ClusterClient.
type RedisCluster struct {
	rdb             *redis.ClusterClient // redis cluster client
	capacity        int                  // capacity of the cache
	itemPoolManager *cache.ItemPoolManager
	keysSetName     string
	Serializer      serializer.ISerializer
}

// NewRedisCluster creates a new Redis Cluster backend with the given options.
func NewRedisCluster(redisOptions ...Option[RedisCluster]) (IBackend[RedisCluster], error) {
	rc := &RedisCluster{itemPoolManager: cache.NewItemPoolManager()}

	ApplyOptions(rc, redisOptions...)

	if rc.rdb == nil {
		return nil, sentinel.ErrNilClient
	}

	if rc.capacity < 0 {
		return nil, sentinel.ErrInvalidCapacity
	}

	if rc.keysSetName == "" {
		rc.keysSetName = constants.RedisBackend
	}

	if rc.Serializer == nil {
		var err error

		rc.Serializer, err = serializer.New("msgpack")
		if err != nil {
			return nil, err
		}
	}

	return rc, nil
}

// SetCapacity sets the capacity of the cluster backend.
func (cacheBackend *RedisCluster) SetCapacity(capacity int) {
	if capacity < 0 {
		return
	}

	cacheBackend.capacity = capacity
}

// Capacity returns the capacity of the cluster backend.
func (cacheBackend *RedisCluster) Capacity() int {
	return cacheBackend.capacity
}

// Count returns the number of keys stored.
func (cacheBackend *RedisCluster) Count(ctx context.Context) int {
	count, err := cacheBackend.rdb.DBSize(ctx).Result()
	if err != nil {
		return 0
	}

	return int(count)
}

// Get retrieves an item by key.
func (cacheBackend *RedisCluster) Get(ctx context.Context, key string) (*cache.Item, bool) {
	isMember, err := cacheBackend.rdb.SIsMember(ctx, cacheBackend.keysSetName, key).Result()
	if err != nil || !isMember {
		return nil, false
	}

	pooled := cacheBackend.itemPoolManager.Get()

	data, err := cacheBackend.rdb.HGet(ctx, key, "data").Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, false
		}

		return nil, false
	}

	err = cacheBackend.Serializer.Unmarshal(data, pooled)
	if err != nil {
		return nil, false
	}

	out := *pooled
	cacheBackend.itemPoolManager.Put(pooled)

	return &out, true
}

// Set stores an item in the cluster.
func (cacheBackend *RedisCluster) Set(ctx context.Context, item *cache.Item) error {
	return redisSet(ctx, cacheBackend.rdb, cacheBackend.keysSetName, item, cacheBackend.Serializer)
}

// List returns items matching optional filters.
func (cacheBackend *RedisCluster) List(ctx context.Context, filters ...IFilter) ([]*cache.Item, error) {
	return redisList(ctx, cacheBackend.rdb, cacheBackend.keysSetName, cacheBackend.Serializer, cacheBackend.itemPoolManager, filters...)
}

// Remove deletes the specified keys.
func (cacheBackend *RedisCluster) Remove(ctx context.Context, keys ...string) error {
	return redisRemove(ctx, cacheBackend.rdb, cacheBackend.keysSetName, keys...)
}

// Clear flushes the database.
func (cacheBackend *RedisCluster) Clear(ctx context.Context) error {
	_, err := cacheBackend.rdb.FlushDB(ctx).Result()

	return ewrap.Wrap(err, "flushing database", ewrap.WithRetry(clusterMaxRetries, clusterRetriesDelay))
}
