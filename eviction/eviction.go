package eviction

import (
	"github.com/hyp3rd/ewrap"

	"github.com/hyp3rd/hypercache/errors"
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

var algorithmRegistry = make(map[string]func(capacity int) (IAlgorithm, error))

// NewEvictionAlgorithm creates a new eviction algorithm with the given capacity.
// If the capacity is negative, it returns an error.
// The algorithmName parameter is used to select the eviction algorithm from the registry.
func NewEvictionAlgorithm(algorithmName string, capacity int) (IAlgorithm, error) {
	// Check the parameters.
	if algorithmName == "" {
		return nil, ewrap.Wrap(errors.ErrParamCannotBeEmpty, "algorithmName")
	}

	if capacity < 0 {
		return nil, ewrap.Wrapf(errors.ErrInvalidCapacity, "capacity")
	}

	createFunc, ok := algorithmRegistry[algorithmName]
	if !ok {
		return nil, ewrap.Wrap(errors.ErrAlgorithmNotFound, algorithmName)
	}

	return createFunc(capacity)
}

// RegisterEvictionAlgorithm registers a new eviction algorithm with the given name.
func RegisterEvictionAlgorithm(name string, createFunc func(capacity int) (IAlgorithm, error)) {
	algorithmRegistry[name] = createFunc
}

// RegisterEvictionAlgorithms registers a set of eviction algorithms.
func RegisterEvictionAlgorithms(algorithms map[string]func(capacity int) (IAlgorithm, error)) {
	for name, createFunc := range algorithms {
		algorithmRegistry[name] = createFunc
	}
}

// Register the default eviction algorithms.
func init() {
	// Define the default eviction algorithms.
	algorithms := map[string]func(capacity int) (IAlgorithm, error){
		"arc": func(capacity int) (IAlgorithm, error) {
			return NewARCAlgorithm(capacity)
		},
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
	// Register the default eviction algorithms.
	RegisterEvictionAlgorithms(algorithms)
}
