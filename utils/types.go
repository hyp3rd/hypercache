package utils

import (
	"fmt"
	"reflect"
	"strings"
	"sync"

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

// func (c *CacheBackendChecker[T]) IsRedisBackend() (backend.RedisBackend, bool) {
//     obj, ok := c.backend.(*backend.RedisBackend)
//     return *obj, ok
// }

type TypeRegistry interface {
	CacheObject(val interface{})
	GetCachedObject(t reflect.Type) (interface{}, reflect.Type)
	IsCached(t reflect.Type) bool
	RemoveObject(t reflect.Type)
	ClearRegistry()
}

type typeRegistry struct {
	registry sync.Map
}

var instance *typeRegistry
var once sync.Once

var GetTypeRegistry = func() TypeRegistry {
	return NewTypeRegistry()
}

func NewTypeRegistry() TypeRegistry {
	once.Do(func() {
		instance = &typeRegistry{
			registry: sync.Map{},
		}
	})
	return instance
}

func (tr *typeRegistry) CacheObject(val interface{}) {
	t := reflect.TypeOf(val)
	tr.registry.Store(t, val)
}

func (tr *typeRegistry) GetCachedObject(t reflect.Type) (interface{}, reflect.Type) {
	if val, ok := tr.registry.Load(t); ok {
		return val, t
	}
	return nil, t
}

func (tr *typeRegistry) IsCached(t reflect.Type) bool {
	_, ok := tr.registry.Load(t)
	return ok
}

func (tr *typeRegistry) RemoveObject(t reflect.Type) {
	tr.registry.Delete(t)
}

func (tr *typeRegistry) ClearRegistry() {
	tr.registry = sync.Map{}
}
