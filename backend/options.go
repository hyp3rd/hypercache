package backend

import (
	"github.com/hyp3rd/hypercache/cache"
	"github.com/hyp3rd/hypercache/types"
)

// BackendOption is a function type that can be used to configure the `HyperCache` struct.
type BackendOption[T any] func(*T)

// FilterOption is a function type that can be used to configure the `Filter` struct.
type FilterOption[T any] func(*T)

func ApplyBackendOptions[T any](backend *T, options ...FilterOption[T]) {
	for _, option := range options {
		option(backend)
	}
}

// WithSortBy is an option that sets the field to sort the items by.
// The field can be any of the fields in the `CacheItem` struct.
func WithSortBy[T any](field types.SortingField) FilterOption[T] {
	return func(a *T) {
		switch filter := any(a).(type) {
		case *InMemoryBackend:
			filter.sortBy = field.String()
		}
	}
}

// WithSortAscending is an option that sets the sort order to ascending.
// When sorting the items in the cache, they will be sorted in ascending order based on the field specified with the `WithSortBy` option.
func WithSortAscending[T any]() FilterOption[T] {
	return func(a *T) {
		switch filter := any(a).(type) {
		case *InMemoryBackend:
			filter.sortAscending = true
		}
	}
}

// WithSortDescending is an option that sets the sort order to descending.
// When sorting the items in the cache, they will be sorted in descending order based on the field specified with the `WithSortBy` option.
func WithSortDescending[T any]() FilterOption[T] {
	return func(a *T) {
		switch filter := any(a).(type) {
		case *InMemoryBackend:
			filter.sortAscending = false
		}
	}
}

// WithFilterFunc is an option that sets the filter function to use.
// The filter function is a predicate that takes a `CacheItem` as an argument and returns a boolean indicating whether the item should be included in the cache.
func WithFilterFunc[T any](fn func(item *cache.CacheItem) bool) FilterOption[T] {
	return func(a *T) {
		switch filter := any(a).(type) {
		case *InMemoryBackend:
			filter.filterFunc = fn
		}
	}
}
