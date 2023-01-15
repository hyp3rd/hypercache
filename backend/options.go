package backend

import (
	"github.com/go-redis/redis/v8"
	"github.com/hyp3rd/hypercache/models"
	"github.com/hyp3rd/hypercache/types"
)

// ISortableBackend is an interface that defines the methods that a backend should implement to be sortable.
type iSortableBackend interface {
	// setSortAscending indicates whether the items should be sorted in ascending order.
	setSortAscending(ascending bool)
	// setSortBy sets the field to sort the items by.
	setSortBy(sortBy string)
}

// setSortAscending sets the `SortAscending` field of the `InMemory` backend.
func (inm *InMemory) setSortAscending(ascending bool) {
	inm.SortAscending = ascending
}

// setSortAscending sets the `SortAscending` field of the `RedisBackend` backend.
func (rb *RedisBackend) setSortAscending(ascending bool) {
	rb.SortAscending = ascending
}

// setSortBy sets the `SortBy` field of the `InMemory` backend.
func (inm *InMemory) setSortBy(sortBy string) {
	inm.SortBy = sortBy
}

// setSortBy sets the `SortBy` field of the `RedisBackend` backend.
func (rb *RedisBackend) setSortBy(sortBy string) {
	rb.SortBy = sortBy
}

// FilterFunc is a predicate that takes a `Item` as an argument and returns a boolean indicating whether the item should be included in the cache.
type FilterFunc func(item *models.Item) bool // filters applied when listing the items in the cache

// IFilterableBackend is an interface that defines the methods that a backend should implement to be filterable.
type IFilterableBackend interface {
	setFilterFunc(filterFunc FilterFunc)
}

// setFilterFunc sets the `FilterFunc` field of the `InMemory` backend.
func (inm *InMemory) setFilterFunc(filterFunc FilterFunc) {
	inm.FilterFunc = filterFunc
}

// setFilterFunc sets the `FilterFunc` field of the `RedisBackend` backend.
func (rb *RedisBackend) setFilterFunc(filterFunc FilterFunc) {
	rb.FilterFunc = filterFunc
}

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
		backend.rdb = client
	}
}

// WithKeysSetName is an option that sets the name of the set that holds the keys of the items in the cache
func WithKeysSetName[T RedisBackend](keysSetName string) Option[RedisBackend] {
	return func(backend *RedisBackend) {
		backend.keysSetName = keysSetName
	}
}

// FilterOption is a function type that can be used to configure the `Filter` struct.
type FilterOption[T any] func(*T)

// ApplyFilterOptions applies the given options to the given filter.
func ApplyFilterOptions[T IBackendConstrain](backend *T, options ...FilterOption[T]) {
	for _, option := range options {
		option(backend)
	}
}

// WithSortBy is an option that sets the field to sort the items by.
// The field can be any of the fields in the `Item` struct.
func WithSortBy[T IBackendConstrain](field types.SortingField) FilterOption[T] {
	return func(a *T) {
		if sortable, ok := any(a).(iSortableBackend); ok {
			sortable.setSortBy(field.String())
		}
	}
}

// WithSortOrderAsc is an option that sets the sort order to ascending or descending.
// When sorting the items in the cache, they will be sorted in ascending or descending order based on the field specified with the `WithSortBy` option.
func WithSortOrderAsc[T IBackendConstrain](ascending bool) FilterOption[T] {
	return func(a *T) {
		if sortable, ok := any(a).(iSortableBackend); ok {
			sortable.setSortAscending(ascending)
		}
	}
}

// WithFilterFunc is an option that sets the filter function to use.
// The filter function is a predicate that takes a `Item` as an argument and returns a boolean indicating whether the item should be included in the cache.
func WithFilterFunc[T any](fn func(item *models.Item) bool) FilterOption[T] {
	return func(a *T) {
		if filterable, ok := any(a).(IFilterableBackend); ok {
			filterable.setFilterFunc(fn)
		}
	}

	// return func(a *T) {
	// 	switch filter := any(a).(type) {
	// 	case *InMemory:
	// 		filter.FilterFunc = fn
	// 	}
	// }
}
