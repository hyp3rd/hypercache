package backend

import (
	"github.com/redis/go-redis/v9"

	"github.com/hyp3rd/hypercache/internal/libs/serializer"
)

// iConfigurableBackend is an interface that defines the methods that a backend should implement to be configurable.
type iConfigurableBackend interface {
	// setCapacity sets the capacity of the cache.
	setCapacity(capacity int)
}

// setCapacity sets the `Capacity` field of the `InMemory` backend.
func (inm *InMemory) setCapacity(capacity int) {
	inm.capacity = capacity
}

// setCapacity sets the `Capacity` field of the `Redis` backend.
func (rb *Redis) setCapacity(capacity int) {
	rb.capacity = capacity
}

// setCapacity sets the `Capacity` field of the `RedisCluster` backend.
func (rc *RedisCluster) setCapacity(capacity int) {
	rc.capacity = capacity
}

// Option is a function type that can be used to configure the `HyperCache` struct.
type Option[T IBackendConstrain] func(*T)

// ApplyOptions applies the given options to the given backend.
func ApplyOptions[T IBackendConstrain](backend *T, options ...Option[T]) {
	for _, option := range options {
		option(backend)
	}
}

// WithCapacity is an option that sets the capacity of the cache.
func WithCapacity[T IBackendConstrain](capacity int) Option[T] {
	return func(a *T) {
		if configurable, ok := any(a).(iConfigurableBackend); ok {
			configurable.setCapacity(capacity)
		}
	}
}

// WithRedisClient is an option that sets the redis client to use.
func WithRedisClient[T Redis](client *redis.Client) Option[Redis] {
	return func(backend *Redis) {
		backend.rdb = client
	}
}

// WithKeysSetName is an option that sets the name of the set that holds the keys of the items in the cache.
func WithKeysSetName[T Redis](keysSetName string) Option[Redis] {
	return func(backend *Redis) {
		backend.keysSetName = keysSetName
	}
}

// WithSerializer is an option that sets the serializer to use. The serializer is used to serialize and deserialize the items in the cache.
//   - The default serializer is `serializer.MsgpackSerializer`.
//   - The `serializer.JSONSerializer` can be used to serialize and deserialize the items in the cache as JSON.
//   - The interface `serializer.ISerializer` can be implemented to use a custom serializer.
func WithSerializer[T Redis](serializer serializer.ISerializer) Option[Redis] {
	return func(backend *Redis) {
		backend.Serializer = serializer
	}
}

// WithRedisClusterClient sets the redis cluster client to use.
func WithRedisClusterClient[T RedisCluster](client *redis.ClusterClient) Option[RedisCluster] {
	return func(backend *RedisCluster) {
		backend.rdb = client
	}
}

// WithClusterKeysSetName sets the name of the set for cluster backend keys.
func WithClusterKeysSetName[T RedisCluster](keysSetName string) Option[RedisCluster] {
	return func(backend *RedisCluster) {
		backend.keysSetName = keysSetName
	}
}

// WithClusterSerializer sets the serializer for the cluster backend.
func WithClusterSerializer[T RedisCluster](ser serializer.ISerializer) Option[RedisCluster] {
	return func(backend *RedisCluster) {
		backend.Serializer = ser
	}
}
