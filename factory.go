package hypercache

import (
	"github.com/hyp3rd/hypercache/backend"
	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/internal/sentinel"
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
		return nil, sentinel.ErrInvalidBackendType
	}

	return backend.NewInMemory(inMemoryConfig.InMemoryOptions...)
}

// RedisBackendConstructor is a backend constructor for Redis.
type RedisBackendConstructor struct{}

// Create creates a new Redis backend.
func (rbc RedisBackendConstructor) Create(config any) (any, error) {
	redisConfig, ok := config.(*Config[backend.Redis])
	if !ok {
		return nil, sentinel.ErrInvalidBackendType
	}

	return backend.NewRedis(redisConfig.RedisOptions...)
}

// BackendManager is a factory for creating HyperCache backend instances.
type BackendManager struct {
	backendRegistry map[string]IBackendConstructor
}

// getDefaultBackends returns the default set of backend constructors.
func getDefaultBackends() map[string]IBackendConstructor {
	return map[string]IBackendConstructor{
		constants.InMemoryBackend: InMemoryBackendConstructor{},
		constants.RedisBackend:    RedisBackendConstructor{},
	}
}

// NewBackendManager creates a new BackendManager with default backends pre-registered.
func NewBackendManager() *BackendManager {
	manager := &BackendManager{
		backendRegistry: make(map[string]IBackendConstructor),
	}
	// Register the default backends
	for name, constructor := range getDefaultBackends() {
		manager.RegisterBackend(name, constructor)
	}

	return manager
}

// NewEmptyBackendManager creates a new BackendManager without default backends.
// This is useful for testing or when you want to register only specific backends.
func NewEmptyBackendManager() *BackendManager {
	return &BackendManager{
		backendRegistry: make(map[string]IBackendConstructor),
	}
}

// RegisterBackend registers a new backend constructor.
func (hcm *BackendManager) RegisterBackend(name string, constructor IBackendConstructor) {
	hcm.backendRegistry[name] = constructor
}

// GetDefaultManager returns a new BackendManager with default backends pre-registered.
// This replaces the previous global instance with a factory function.
func GetDefaultManager() *BackendManager {
	return NewBackendManager()
}
