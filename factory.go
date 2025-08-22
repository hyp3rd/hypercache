package hypercache

import (
	"context"

	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/pkg/backend"
)

// IBackendConstructor is an interface for backend constructors with type safety.
// It returns a typed backend.IBackend[T] instead of any.
type IBackendConstructor[T backend.IBackendConstrain] interface {
	Create(ctx context.Context, cfg *Config[T]) (backend.IBackend[T], error)
}

// InMemoryBackendConstructor constructs InMemory backends.
type InMemoryBackendConstructor struct{}

// Create creates a new InMemory backend.
func (InMemoryBackendConstructor) Create(_ context.Context, cfg *Config[backend.InMemory]) (backend.IBackend[backend.InMemory], error) {
	return backend.NewInMemory(cfg.InMemoryOptions...)
}

// RedisBackendConstructor constructs Redis backends.
type RedisBackendConstructor struct{}

// Create creates a new Redis backend.
func (RedisBackendConstructor) Create(_ context.Context, cfg *Config[backend.Redis]) (backend.IBackend[backend.Redis], error) {
	return backend.NewRedis(cfg.RedisOptions...)
}

// RedisClusterBackendConstructor constructs Redis Cluster backends.
type RedisClusterBackendConstructor struct{}

// Create creates a new Redis Cluster backend.
func (RedisClusterBackendConstructor) Create(
	_ context.Context,
	cfg *Config[backend.RedisCluster],
) (backend.IBackend[backend.RedisCluster], error) {
	return backend.NewRedisCluster(cfg.RedisClusterOptions...)
}

// DistMemoryBackendConstructor constructs DistMemory backends.
type DistMemoryBackendConstructor struct{}

// Create creates a new DistMemory backend.
func (DistMemoryBackendConstructor) Create(
	ctx context.Context,
	_ *Config[backend.DistMemory],
) (backend.IBackend[backend.DistMemory], error) {
	return backend.NewDistMemory(ctx)
}

// BackendManager is a factory for creating HyperCache backend instances.
// It maintains a registry of backend constructors. We store them as any internally,
// and cast to the typed constructor at use site based on T.
type BackendManager struct {
	backendRegistry map[string]any
}

// getDefaultBackends returns the default set of backend constructors.
func getDefaultBackends() map[string]any {
	return map[string]any{
		constants.InMemoryBackend:     InMemoryBackendConstructor{},
		constants.RedisBackend:        RedisBackendConstructor{},
		constants.RedisClusterBackend: RedisClusterBackendConstructor{},
		constants.DistMemoryBackend:   DistMemoryBackendConstructor{},
	}
}

// NewBackendManager creates a new BackendManager with default backends pre-registered.
func NewBackendManager() *BackendManager {
	manager := &BackendManager{
		backendRegistry: make(map[string]any),
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
		backendRegistry: make(map[string]any),
	}
}

// RegisterBackend registers a new backend constructor. The constructor should be
// a value implementing IBackendConstructor[T] for some T; stored as any.
func (hcm *BackendManager) RegisterBackend(name string, constructor any) {
	hcm.backendRegistry[name] = constructor
}

// GetDefaultManager returns a new BackendManager with default backends pre-registered.
// This replaces the previous global instance with a factory function.
func GetDefaultManager() *BackendManager { return NewBackendManager() }
