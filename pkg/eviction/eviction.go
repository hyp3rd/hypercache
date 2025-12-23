// Package eviction implements various cache eviction algorithms.
package eviction

import (
	"maps"

	"github.com/hyp3rd/ewrap"

	"github.com/hyp3rd/hypercache/internal/sentinel"
)

// IAlgorithm is the interface that must be implemented by eviction algorithms.
type IAlgorithm interface {
	// Evict returns the next item to be evicted from the cache.
	Evict() (string, bool)
	// Set adds a new item to the cache with the given key.
	Set(key string, value any)
	// Get retrieves the item with the given key from the cache.
	Get(key string) (any, bool)
	// Delete removes the item with the given key from the cache.
	Delete(key string)
}

// AlgorithmRegistry manages eviction algorithm constructors.
type AlgorithmRegistry struct {
	algorithms map[string]func(capacity int) (IAlgorithm, error)
}

// getDefaultAlgorithms returns the default set of eviction algorithms.
func getDefaultAlgorithms() map[string]func(capacity int) (IAlgorithm, error) {
	return map[string]func(capacity int) (IAlgorithm, error){
		"lru": func(capacity int) (IAlgorithm, error) {
			return NewLRUAlgorithm(capacity)
		},
		"clock": func(capacity int) (IAlgorithm, error) {
			return NewClockAlgorithm(capacity)
		},
		"lfu": func(capacity int) (IAlgorithm, error) {
			return NewLFUAlgorithm(capacity)
		},
		"cawolfu": func(capacity int) (IAlgorithm, error) {
			return NewCAWOLFU(capacity)
		},
	}
}

// NewAlgorithmRegistry creates a new algorithm registry.
func NewAlgorithmRegistry() *AlgorithmRegistry {
	registry := &AlgorithmRegistry{
		algorithms: make(map[string]func(capacity int) (IAlgorithm, error)),
	}
	// Register the default algorithms
	registry.RegisterMultiple(getDefaultAlgorithms())

	return registry
}

// NewEmptyAlgorithmRegistry creates a new algorithm registry without default algorithms.
// This is useful for testing or when you want to register only specific algorithms.
func NewEmptyAlgorithmRegistry() *AlgorithmRegistry {
	return &AlgorithmRegistry{
		algorithms: make(map[string]func(capacity int) (IAlgorithm, error)),
	}
}

// Register registers a new eviction algorithm with the given name.
func (r *AlgorithmRegistry) Register(name string, createFunc func(capacity int) (IAlgorithm, error)) {
	r.algorithms[name] = createFunc
}

// RegisterMultiple registers a set of eviction algorithms.
func (r *AlgorithmRegistry) RegisterMultiple(algorithms map[string]func(capacity int) (IAlgorithm, error)) {
	maps.Copy(r.algorithms, algorithms)
}

// NewAlgorithm creates a new eviction algorithm with the given capacity.
func (r *AlgorithmRegistry) NewAlgorithm(algorithmName string, capacity int) (IAlgorithm, error) {
	// Check the parameters.
	if algorithmName == "" {
		return nil, ewrap.Wrap(sentinel.ErrParamCannotBeEmpty, "algorithmName")
	}

	if capacity < 0 {
		return nil, ewrap.Wrapf(sentinel.ErrInvalidCapacity, "capacity")
	}

	createFunc, ok := r.algorithms[algorithmName]
	if !ok {
		return nil, ewrap.Wrap(sentinel.ErrAlgorithmNotFound, algorithmName)
	}

	return createFunc(capacity)
}

// NewEvictionAlgorithm creates a new eviction algorithm with the given capacity.
// It uses a new registry instance with default algorithms for each call.
// If the capacity is negative, it returns an error.
// The algorithmName parameter is used to select the eviction algorithm from the default algorithms.
func NewEvictionAlgorithm(algorithmName string, capacity int) (IAlgorithm, error) {
	registry := NewAlgorithmRegistry()

	return registry.NewAlgorithm(algorithmName, capacity)
}
