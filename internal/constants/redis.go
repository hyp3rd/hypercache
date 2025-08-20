package constants

import "time"

const (
	// RedisKeySetName is the name of the Redis key set.
	RedisKeySetName = "hypercache"
	// RedisDialTimeout is the timeout for the Redis dialer.
	RedisDialTimeout = 10 * time.Second
	// RedisClientMaxRetries is the maximum number of retries for the Redis client.
	RedisClientMaxRetries = 10
	// RedisClientReadTimeout is the read timeout for the Redis client.
	RedisClientReadTimeout = 30 * time.Second
	// RedisClientWriteTimeout is the write timeout for the Redis client.
	RedisClientWriteTimeout = 30 * time.Second
	// RedisClientPoolTimeout is the pool timeout for the Redis client.
	RedisClientPoolTimeout = 30 * time.Second
	// RedisClientPoolSize is the pool size for the Redis client.
	RedisClientPoolSize = 20
	// RedisClientMinIdleConns is the minimum number of idle connections for the Redis client.
	RedisClientMinIdleConns = 10
)
