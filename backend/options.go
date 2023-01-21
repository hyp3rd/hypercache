package backend

import (
	"github.com/go-redis/redis/v9"
	"github.com/hyp3rd/hypercache/libs/serializer"
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

// setSortAscending sets the `SortAscending` field of the `Redis` backend.
func (rb *Redis) setSortAscending(ascending bool) {
	rb.SortAscending = ascending
}

// setSortBy sets the `SortBy` field of the `InMemory` backend.
func (inm *InMemory) setSortBy(sortBy string) {
	inm.SortBy = sortBy
}

// setSortBy sets the `SortBy` field of the `Redis` backend.
func (rb *Redis) setSortBy(sortBy string) {
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

// setFilterFunc sets the `FilterFunc` field of the `Redis` backend.
func (rb *Redis) setFilterFunc(filterFunc FilterFunc) {
	rb.FilterFunc = filterFunc
}

// iConfigurableBackend is an interface that defines the methods that a backend should implement to be configurable.
type iConfigurableBackend interface {
	// setCapacity sets the capacity of the cache.
	setCapacity(capacity int)
	// setMaxCacheSize sets the maximum size of the cache.
	setMaxCacheSize(maxCacheSize int)
}

// setCapacity sets the `Capacity` field of the `InMemory` backend.
func (inm *InMemory) setCapacity(capacity int) {
	inm.capacity = capacity
}

// setCapacity sets the `Capacity` field of the `Redis` backend.
func (rb *Redis) setCapacity(capacity int) {
	rb.capacity = capacity
}

// setMaxCacheSize sets the `maxCacheSize` field of the `InMemory` backend.
func (inm *InMemory) setMaxCacheSize(maxCacheSize int) {
	inm.maxCacheSize = maxCacheSize
}

func (rb *Redis) setMaxCacheSize(maxCacheSize int) {
	rb.maxCacheSize = maxCacheSize
}

// Option is a function type that can be used to configure the `HyperCache` struct.
type Option[T IBackendConstrain] func(*T)

// ApplyOptions applies the given options to the given backend.
func ApplyOptions[T IBackendConstrain](backend *T, options ...Option[T]) {
	for _, option := range options {
		option(backend)
	}
}

// WithMaxCacheSize is an option that sets the maximum size of the cache.
// The maximum size of the cache is the maximum number of items that can be stored in the cache.
// If the maximum size of the cache is reached, the least recently used item will be evicted from the cache.
func WithMaxCacheSize[T IBackendConstrain](maxCacheSize int) Option[T] {
	return func(a *T) {
		if configurable, ok := any(a).(iConfigurableBackend); ok {
			configurable.setMaxCacheSize(maxCacheSize)
		}
	}
}

// WithCapacity is an option that sets the capacity of the cache.
func WithCapacity[T IBackendConstrain](capacity int) Option[T] {
	return func(a *T) {
		if configurable, ok := any(a).(iConfigurableBackend); ok {
			configurable.setCapacity(capacity)
		}
	}
}

// WithRedisClient is an option that sets the redis client to use.
func WithRedisClient[T Redis](client *redis.Client) Option[Redis] {
	return func(backend *Redis) {
		backend.rdb = client
	}
}

// WithKeysSetName is an option that sets the name of the set that holds the keys of the items in the cache
func WithKeysSetName[T Redis](keysSetName string) Option[Redis] {
	return func(backend *Redis) {
		backend.keysSetName = keysSetName
	}
}

// WithSerializer is an option that sets the serializer to use. The serializer is used to serialize and deserialize the items in the cache.
//   - The default serializer is `serializer.MsgpackSerializer`.
//   - The `serializer.JSONSerializer` can be used to serialize and deserialize the items in the cache as JSON.
//   - The interface `serializer.ISerializer` can be implemented to use a custom serializer.
func WithSerializer[T Redis](serializer serializer.ISerializer) Option[Redis] {
	return func(backend *Redis) {
		backend.Serializer = serializer
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
}
