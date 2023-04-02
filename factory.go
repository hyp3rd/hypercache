package hypercache

import (
	"github.com/hyp3rd/hypercache/backend"
	"github.com/hyp3rd/hypercache/errors"
)

// IBackendConstructor is an interface for backend constructors
type IBackendConstructor interface {
	Create(config interface{}) (interface{}, error)
}

// InMemoryBackendConstructor is a backend constructor for InMemory
type InMemoryBackendConstructor struct{}

// Create creates a new InMemory backend
func (ibc InMemoryBackendConstructor) Create(config interface{}) (interface{}, error) {
	inMemoryConfig, ok := config.(*Config[backend.InMemory])
	if !ok {
		return nil, errors.ErrInvalidBackendType
	}
	return backend.NewInMemory(inMemoryConfig.InMemoryOptions...)
}

// RedisBackendConstructor is a backend constructor for Redis
type RedisBackendConstructor struct{}

// Create creates a new Redis backend
func (rbc RedisBackendConstructor) Create(config interface{}) (interface{}, error) {
	redisConfig, ok := config.(*Config[backend.Redis])
	if !ok {
		return nil, errors.ErrInvalidBackendType
	}
	return backend.NewRedis(redisConfig.RedisOptions...)
}

// HyperCacheManager is a factory for creating HyperCache backend instances
type HyperCacheManager struct {
	backendRegistry map[string]IBackendConstructor
}

// NewHyperCacheManager creates a new HyperCacheManager
func NewHyperCacheManager() *HyperCacheManager {
	return &HyperCacheManager{
		backendRegistry: make(map[string]IBackendConstructor),
	}
}

// RegisterBackend registers a new backend constructor
func (hcm *HyperCacheManager) RegisterBackend(name string, constructor IBackendConstructor) {
	hcm.backendRegistry[name] = constructor
}

var defaultManager *HyperCacheManager

// GetDefaultManager returns the default HyperCacheManager
func GetDefaultManager() *HyperCacheManager {
	return defaultManager
}

func init() {
	defaultManager = NewHyperCacheManager()
	defaultManager.RegisterBackend("in-memory", InMemoryBackendConstructor{})
	defaultManager.RegisterBackend("redis", RedisBackendConstructor{})
}
