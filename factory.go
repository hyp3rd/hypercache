package hypercache

import (
	"github.com/hyp3rd/hypercache/backend"
	"github.com/hyp3rd/hypercache/errors"
)

// BackendConstructor is a function that returns a new backend instance
type BackendConstructor[T backend.IBackendConstrain] func(config *Config[T]) (backend.IBackend[T], error)

// RegisterBackend registers a new backend constructor
var backendRegistry = make(map[string]interface{})

// RegisterBackend registers a new backend constructor with the given name.
func RegisterBackend[T backend.IBackendConstrain](name string, constructor BackendConstructor[T]) {
	backendRegistry[name] = constructor
}

// GetBackend returns a new backend instance with the given name.
func GetBackend[T backend.IBackendConstrain](name string, config *Config[T]) (backend.IBackend[T], error) {
	if constructor, ok := backendRegistry[name]; ok {
		return constructor.(BackendConstructor[T])(config)
	}
	return nil, errors.ErrBackendNotFound
}

// Wrapper functions for the constructors
func newInMemoryWrapper(config *Config[backend.InMemory]) (backend.IBackend[backend.InMemory], error) {
	return backend.NewInMemory(config.InMemoryOptions...)
}

func newRedisWrapper(config *Config[backend.Redis]) (backend.IBackend[backend.Redis], error) {
	return backend.NewRedis(config.RedisOptions...)
}

// Register the default backends.
func init() {
	RegisterBackend("in-memory", newInMemoryWrapper)
	RegisterBackend("redis", newRedisWrapper)
}
