// Package introspect provides utilities for runtime inspection and type checking
// of cache backends. It includes helpers to determine the specific implementation
// type of cache backends at runtime, enabling conditional logic based on the
// underlying storage mechanism.
package introspect

import (
	"github.com/hyp3rd/hypercache/pkg/backend"
)

// CacheBackendChecker is a generic helper struct that provides runtime type checking
// capabilities for cache backends. It allows inspection of the underlying backend
// implementation to determine its specific type (e.g., InMemory, Redis) without
// requiring direct type assertions in client code.
//
// The generic type parameter T must satisfy the backend.IBackendConstrain interface,
// ensuring type safety across different backend implementations.
//
// Example usage:
//
//	checker := &CacheBackendChecker[string]{
//	    Backend:     myBackend,
//	    BackendType: "redis",
//	}
//	if checker.IsRedis() {
//	    // Handle Redis-specific logic
//	}
type CacheBackendChecker[T backend.IBackendConstrain] struct {
	Backend     backend.IBackend[T]
	BackendType string
}

// IsInMemory returns true if the backend is an InMemory.
func (c *CacheBackendChecker[T]) IsInMemory() bool {
	_, ok := c.Backend.(*backend.InMemory)

	return ok
}

// IsRedis returns true if the backend is a Redis.
func (c *CacheBackendChecker[T]) IsRedis() bool {
	_, ok := c.Backend.(*backend.Redis)

	return ok
}

// GetRegisteredType returns the backend type as a string.
func (c *CacheBackendChecker[T]) GetRegisteredType() string {
	return c.BackendType
}
