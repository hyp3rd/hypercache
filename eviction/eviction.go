package eviction

import (
	"fmt"

	"github.com/hyp3rd/hypercache/errors"
)

// Algorithm is the interface that must be implemented by eviction algorithms.
type Algorithm interface {
	// Evict returns the next item to be evicted from the cache.
	Evict() (string, bool)
	// Set adds a new item to the cache with the given key.
	Set(key string, value any)
	// Get retrieves the item with the given key from the cache.
	Get(key string) (any, bool)
	// Delete removes the item with the given key from the cache.
	Delete(key string)
}

var algorithmRegistry = make(map[string]func(capacity int) (Algorithm, error))

// NewEvictionAlgorithm creates a new eviction algorithm with the given capacity.
// If the capacity is negative, it returns an error.
// The algorithmName parameter is used to select the eviction algorithm from the registry.
func NewEvictionAlgorithm(algorithmName string, capacity int) (Algorithm, error) {
	// Check the parameters.
	if algorithmName == "" {
		return nil, fmt.Errorf("%s: %s", errors.ErrParamCannotBeEmpty, "algorithmName")
	}
	if capacity < 0 {
		return nil, errors.ErrInvalidCapacity
	}

	createFunc, ok := algorithmRegistry[algorithmName]
	if !ok {
		return nil, fmt.Errorf("%s: %s", errors.ErrAlgorithmNotFound, algorithmName)
	}

	return createFunc(capacity)
}

// RegisterEvictionAlgorithm registers a new eviction algorithm with the given name.
func RegisterEvictionAlgorithm(name string, createFunc func(capacity int) (Algorithm, error)) {
	algorithmRegistry[name] = createFunc
}

// Register the default eviction algorithms.
func init() {
	RegisterEvictionAlgorithm("arc", func(capacity int) (Algorithm, error) {
		return NewARC(capacity)
	})
	RegisterEvictionAlgorithm("lru", func(capacity int) (Algorithm, error) {
		return NewLRU(capacity)
	})
	RegisterEvictionAlgorithm("clock", func(capacity int) (Algorithm, error) {
		return NewClockAlgorithm(capacity)
	})
	RegisterEvictionAlgorithm("lfu", func(capacity int) (Algorithm, error) {
		return NewLFUAlgorithm(capacity)
	})
	RegisterEvictionAlgorithm("cawolfu", func(capacity int) (Algorithm, error) {
		return NewCAWOLFU(capacity)
	})
}
