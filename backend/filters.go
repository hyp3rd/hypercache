package backend

import (
	"sort"

	"github.com/hyp3rd/hypercache/types"
)

// IFilter is a backend agnostic interface for a filter that can be applied to a list of items.
type IFilter interface {
	ApplyFilter(backendType string, items []*types.Item) []*types.Item
}

// sortByFilter is a filter that sorts the items by a given field.
type sortByFilter struct {
	field string
}

// ApplyFilter applies the sort filter to the given list of items.
func (f sortByFilter) ApplyFilter(backendType string, items []*types.Item) []*types.Item {
	var sorter sort.Interface
	switch f.field {
	case types.SortByKey.String():
		sorter = &itemSorterByKey{items: items}
	case types.SortByLastAccess.String():
		sorter = &itemSorterByLastAccess{items: items}
	case types.SortByAccessCount.String():
		sorter = &itemSorterByAccessCount{items: items}
	case types.SortByExpiration.String():
		sorter = &itemSorterByExpiration{items: items}
	default:
		return items
	}
	sort.Sort(sorter)
	return items
}

// SortOrderFilter is a filter that sorts the items by a given field.
type SortOrderFilter struct {
	ascending bool
}

// ApplyFilter applies the sort order filter to the given list of items.
func (f SortOrderFilter) ApplyFilter(backendType string, items []*types.Item) []*types.Item {
	if !f.ascending {
		sort.Slice(items, func(i, j int) bool {
			return items[j].Key > items[i].Key
		})
	} else {
		sort.Slice(items, func(i, j int) bool {
			return items[i].Key < items[j].Key
		})
	}
	return items
}

// filterFunc is a filter that filters the items by a given field's value.
type filterFunc struct {
	fn func(item *types.Item) bool
}

// ApplyFilter applies the filter function to the given list of items.
func (f filterFunc) ApplyFilter(backendType string, items []*types.Item) []*types.Item {
	filteredItems := make([]*types.Item, 0)
	for _, item := range items {
		if f.fn(item) {
			filteredItems = append(filteredItems, item)
		}
	}
	return filteredItems
}

// WithSortBy returns a filter that sorts the items by a given field.
func WithSortBy(field string) IFilter {
	return sortByFilter{field: field}
}

// WithSortOrderAsc returns a filter that determins whether to sort ascending or not.
func WithSortOrderAsc(ascending bool) SortOrderFilter {
	return SortOrderFilter{ascending: ascending}
}

// WithFilterFunc returns a filter that filters the items by a given field's value.
func WithFilterFunc(fn func(item *types.Item) bool) IFilter {
	return filterFunc{fn: fn}
}
