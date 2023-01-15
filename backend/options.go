package backend

import (
	"github.com/go-redis/redis"
	"github.com/hyp3rd/hypercache/models"
	"github.com/hyp3rd/hypercache/types"
)

// Option is a function type that can be used to configure the `HyperCache` struct.
type Option[T IBackendConstrain] func(*T)

// ApplyOptions applies the given options to the given backend.
func ApplyOptions[T IBackendConstrain](backend *T, options ...Option[T]) {
	for _, option := range options {
		option(backend)
	}
}

// WithCapacity is an option that sets the capacity of the cache.
func WithCapacity[T InMemory](capacity int) Option[InMemory] {
	return func(backend *InMemory) {
		backend.capacity = capacity
	}
}

// WithRedisClient is an option that sets the redis client to use.
func WithRedisClient[T RedisBackend](client *redis.Client) Option[RedisBackend] {
	return func(backend *RedisBackend) {
		backend.client = client
	}
}

// FilterOption is a function type that can be used to configure the `Filter` struct.
type FilterOption[T any] func(*T)

// ApplyFilterOptions applies the given options to the given filter.
func ApplyFilterOptions[T any](backend *T, options ...FilterOption[T]) {
	for _, option := range options {
		option(backend)
	}
}

// WithSortBy is an option that sets the field to sort the items by.
// The field can be any of the fields in the `Item` struct.
func WithSortBy[T any](field types.SortingField) FilterOption[T] {
	return func(a *T) {
		switch filter := any(a).(type) {
		case *InMemory:
			filter.SortBy = field.String()
		}
	}
}

// WithSortAscending is an option that sets the sort order to ascending.
// When sorting the items in the cache, they will be sorted in ascending order based on the field specified with the `WithSortBy` option.
func WithSortAscending[T any]() FilterOption[T] {
	return func(a *T) {
		switch filter := any(a).(type) {
		case *InMemory:
			filter.SortAscending = true
		}
	}
}

// WithSortDescending is an option that sets the sort order to descending.
// When sorting the items in the cache, they will be sorted in descending order based on the field specified with the `WithSortBy` option.
func WithSortDescending[T any]() FilterOption[T] {
	return func(a *T) {
		switch filter := any(a).(type) {
		case *InMemory:
			filter.SortAscending = false
		}
	}
}

// WithFilterFunc is an option that sets the filter function to use.
// The filter function is a predicate that takes a `Item` as an argument and returns a boolean indicating whether the item should be included in the cache.
func WithFilterFunc[T any](fn func(item *models.Item) bool) FilterOption[T] {
	return func(a *T) {
		switch filter := any(a).(type) {
		case *InMemory:
			filter.FilterFunc = fn
		}
	}
}
