package utils

import (
	"fmt"
	"strings"

	"github.com/hyp3rd/hypercache/backend"
)

// TypeName returns the type and inferred type name of the object passed in.
func TypeName(object interface{}) (typeName string, inferredType string) {
	typeString := fmt.Sprintf("%T", object)
	parts := strings.Split(typeString, "[")

	typeName = strings.TrimPrefix(parts[0], "*")

	inferredType = ""
	if len(parts) > 1 {
		inferredType = strings.TrimSuffix(parts[1], "]")
	}
	return typeName, inferredType
}

// CacheBackendChecker is a helper struct to check the type of the backend
type CacheBackendChecker[T backend.IBackendConstrain] struct {
	Backend backend.IBackend[T]
}

// IsInMemory returns true if the backend is an InMemory
func (c *CacheBackendChecker[T]) IsInMemory() bool {
	_, ok := c.Backend.(*backend.InMemory)
	return ok
}

// IsRedisBackend returns true if the backend is a Redis
func (c *CacheBackendChecker[T]) IsRedis() bool {
	_, ok := c.Backend.(*backend.Redis)
	return ok
}
