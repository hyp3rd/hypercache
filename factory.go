package hypercache

import (
	"github.com/hyp3rd/hypercache/backend"
	"github.com/hyp3rd/hypercache/errors"
)

// IBackendConstructor is an interface for backend constructors.
type IBackendConstructor interface {
	Create(config any) (any, error)
}

// InMemoryBackendConstructor is a backend constructor for InMemory.
type InMemoryBackendConstructor struct{}

// Create creates a new InMemory backend.
func (ibc InMemoryBackendConstructor) Create(config any) (any, error) {
	inMemoryConfig, ok := config.(*Config[backend.InMemory])
	if !ok {
		return nil, errors.ErrInvalidBackendType
	}

	return backend.NewInMemory(inMemoryConfig.InMemoryOptions...)
}

// RedisBackendConstructor is a backend constructor for Redis.
type RedisBackendConstructor struct{}

// Create creates a new Redis backend.
func (rbc RedisBackendConstructor) Create(config any) (any, error) {
	redisConfig, ok := config.(*Config[backend.Redis])
	if !ok {
		return nil, errors.ErrInvalidBackendType
	}

	return backend.NewRedis(redisConfig.RedisOptions...)
}

// BackendManager is a factory for creating HyperCache backend instances.
type BackendManager struct {
	backendRegistry map[string]IBackendConstructor
}

// NewBackendManager creates a new BackendManager.
func NewBackendManager() *BackendManager {
	return &BackendManager{
		backendRegistry: make(map[string]IBackendConstructor),
	}
}

// RegisterBackend registers a new backend constructor.
func (hcm *BackendManager) RegisterBackend(name string, constructor IBackendConstructor) {
	hcm.backendRegistry[name] = constructor
}

var defaultManager *BackendManager

// GetDefaultManager returns the default BackendManager.
func GetDefaultManager() *BackendManager {
	return defaultManager
}

func init() {
	defaultManager = NewBackendManager()
	defaultManager.RegisterBackend("in-memory", InMemoryBackendConstructor{})
	defaultManager.RegisterBackend("redis", RedisBackendConstructor{})
}
