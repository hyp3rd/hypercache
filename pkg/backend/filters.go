package backend

import (
	"sort"

	"github.com/hyp3rd/ewrap"

	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/pkg/cache"
)

// itemSorter is a custom sorter for the items.
type itemSorter struct {
	items []*cache.Item
	less  func(i, j *cache.Item) bool
}

func (s *itemSorter) Len() int           { return len(s.items) }
func (s *itemSorter) Swap(i, j int)      { s.items[i], s.items[j] = s.items[j], s.items[i] }
func (s *itemSorter) Less(i, j int) bool { return s.less(s.items[i], s.items[j]) }

// IFilter is a backend agnostic interface for a filter that can be applied to a list of items.
type IFilter interface {
	ApplyFilter(backendType string, items []*cache.Item) ([]*cache.Item, error)
}

// sortByFilter is a filter that sorts the items by a given field.
type sortByFilter struct {
	field string
}

// SortOrderFilter is a filter that sorts the items by a given field.
type SortOrderFilter struct {
	ascending bool
}

// filterFunc is a filter that filters the items by a given field's value.
type filterFunc struct {
	fn func(item *cache.Item) bool
}

// WithSortBy returns a filter that sorts the items by a given field.
func WithSortBy(field string) IFilter {
	return sortByFilter{field: field}
}

// WithSortOrderAsc returns a filter that determines whether to sort ascending or not.
func WithSortOrderAsc(ascending bool) SortOrderFilter {
	return SortOrderFilter{ascending: ascending}
}

// WithFilterFunc returns a filter that filters the items by a given field's value.
func WithFilterFunc(fn func(item *cache.Item) bool) IFilter {
	return filterFunc{fn: fn}
}

// ApplyFilter applies the sort by filter to the given list of items.
func (f sortByFilter) ApplyFilter(_ string, items []*cache.Item) ([]*cache.Item, error) {
	var sorter *itemSorter

	switch f.field {
	case constants.SortByKey.String():
		sorter = &itemSorter{
			items: items,
			less: func(i, j *cache.Item) bool {
				return i.Key < j.Key
			},
		}
	case constants.SortByLastAccess.String():
		sorter = &itemSorter{
			items: items,
			less: func(i, j *cache.Item) bool {
				return i.LastAccess.UnixNano() < j.LastAccess.UnixNano()
			},
		}
	case constants.SortByAccessCount.String():
		sorter = &itemSorter{
			items: items,
			less: func(i, j *cache.Item) bool {
				return i.AccessCount < j.AccessCount
			},
		}
	case constants.SortByExpiration.String():
		sorter = &itemSorter{
			items: items,
			less: func(i, j *cache.Item) bool {
				return i.Expiration < j.Expiration
			},
		}
	default:
		return nil, ewrap.Newf("invalid sort field: %s", f.field)
	}

	sort.Sort(sorter)

	return items, nil
}

// ApplyFilter applies the sort order filter to the given list of items.
func (f SortOrderFilter) ApplyFilter(_ string, items []*cache.Item) ([]*cache.Item, error) {
	if !f.ascending {
		for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
			items[i], items[j] = items[j], items[i]
		}
	}

	return items, nil
}

// ApplyFilter applies the filter function to the given list of items.
func (f filterFunc) ApplyFilter(_ string, items []*cache.Item) ([]*cache.Item, error) {
	filteredItems := make([]*cache.Item, 0)

	for _, item := range items {
		if f.fn(item) {
			filteredItems = append(filteredItems, item)
		}
	}

	return filteredItems, nil
}
