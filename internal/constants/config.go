// Package constants defines default configuration values and backend types
// for the hypercache system. It provides standard settings for cache expiration,
// eviction intervals, algorithms, and supported backend storage types.
package constants

import "time"

const (

	// DefaultExpirationInterval is the default duration for cache item expiration.
	// Items in the cache will be considered expired after this duration if not
	// explicitly set otherwise.
	DefaultExpirationInterval = 30 * time.Minute
	// DefaultEvictionInterval is the default duration for cache eviction.
	// The cache will run its eviction process at this interval to remove
	// expired or least recently used items based on the configured algorithm.
	DefaultEvictionInterval = 10 * time.Minute
	// DefaultEvictionAlgorithm is the default eviction algorithm to use.
	// Specifies which algorithm (LRU - Least Recently Used) should be applied
	// when the cache needs to remove items to free up space.
	DefaultEvictionAlgorithm = "lru"
	// InMemoryBackend is the in-memory backend type.
	// Constant identifier for the in-memory storage backend implementation
	// that stores cache data directly in application memory.
	InMemoryBackend = "in-memory"
	// RedisBackend is the name of the Redis backend.
	// Constant identifier for the Redis storage backend implementation
	// that persists cache data in a Redis database server.
	RedisBackend = "redis"
	// RedisClusterBackend is the name of the Redis Cluster backend.
	// Constant identifier for the Redis Cluster storage backend implementation
	// that persists cache data across a Redis Cluster.
	RedisClusterBackend = "redis-cluster"
	// DistMemoryBackend is the name of the distributed in-memory backend (multi-node in-process simulation).
	DistMemoryBackend = "dist-memory"
)
