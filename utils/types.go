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

type CacheBackendChecker[T backend.IBackendConstrain] struct {
	Backend backend.IBackend[T]
}

// func (c *CacheBackendChecker[T]) CheckType() {
// 	switch reflect.TypeOf(c.backend) {
// 	case reflect.TypeOf(&backend.InMemoryBackend{}):
// 		fmt.Println("cacheBackend is of type InMemoryBackend")
// 	default:
// 		fmt.Println("cacheBackend is of unknown type")
// 	}
// }

func (c *CacheBackendChecker[T]) IsInMemoryBackend() bool {
	_, ok := c.Backend.(*backend.InMemoryBackend)
	return ok
}

func (c *CacheBackendChecker[T]) IsRedisBackend() bool {
	_, ok := c.Backend.(*backend.RedisBackend)
	return ok
}

// func (c *CacheBackendChecker[T]) IsRedisBackend() (backend.RedisBackend, bool) {
//     obj, ok := c.backend.(*backend.RedisBackend)
//     return *obj, ok
// }
