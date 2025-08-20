package constants

import "time"

const (
	// DefaultExpirationInterval is the default duration for cache item expiration.
	DefaultExpirationInterval = 30 * time.Minute
	// DefaultEvictionInterval is the default duration for cache eviction.
	DefaultEvictionInterval = 10 * time.Minute
	// DefaultEvictionAlgorithm is the default eviction algorithm to use.
	DefaultEvictionAlgorithm = "lru"
	// InMemoryBackend is the in-memory backend type.
	InMemoryBackend = "in-memory"
	// RedisBackend is the name of the Redis backend.
	RedisBackend = "redis"
)
