package hypercache

import (
	"time"

	"github.com/hyp3rd/hypercache/cache"
	"github.com/hyp3rd/hypercache/stats"
)

// Service is the service interface for the HyperCache.
// It enables middleware to be added to the service.
type Service interface {
	// Get retrieves a value from the cache using the key
	Get(key string) (value interface{}, ok bool)
	// Set stores a value in the cache using the key and expiration duration
	Set(key string, value any, expiration time.Duration) error
	// GetOrSet retrieves a value from the cache using the key, if the key does not exist, it will set the value using the key and expiration duration
	GetOrSet(key string, value any, expiration time.Duration) (any, error)
	// GetMultiple retrieves a list of values from the cache using the keys
	GetMultiple(keys ...string) (result map[string]any, failed map[string]error)
	// List returns a list of all items in the cache
	List(filters ...any) ([]*cache.Item, error)
	// Remove removes a value from the cache using the key
	Remove(keys ...string)
	// Clear removes all values from the cache
	Clear() error
	// Capacity returns the capacity of the cache
	Capacity() int
	// Size returns the number of items in the cache
	Size() int
	// TriggerEviction triggers the eviction of the cache
	TriggerEviction()
	// Stop stops the cache
	Stop()
	// GetStats returns the stats of the cache
	GetStats() stats.Stats
}

// Middleware describes a service middleware.
type Middleware func(Service) Service

// ApplyMiddleware applies middlewares to a service.
func ApplyMiddleware(svc Service, mw ...Middleware) Service {
	// Apply each middleware in the chain
	for _, m := range mw {
		svc = m(svc)
	}
	// Return the decorated service
	return svc
}
