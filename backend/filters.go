package backend

import (
	"sort"

	"github.com/hyp3rd/hypercache/models"
	"github.com/hyp3rd/hypercache/types"
)

type IFilter interface {
	ApplyFilter(backendType string, items []*models.Item) []*models.Item
}

type sortByFilter struct {
	field string
}

func (f sortByFilter) ApplyFilter(backendType string, items []*models.Item) []*models.Item {
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

type sortOrderFilter struct {
	ascending bool
}

func (f sortOrderFilter) ApplyFilter(backendType string, items []*models.Item) []*models.Item {
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

type filterFuncFilter struct {
	fn func(item *models.Item) bool
}

func (f filterFuncFilter) ApplyFilter(backendType string, items []*models.Item) []*models.Item {
	filteredItems := make([]*models.Item, 0)
	for _, item := range items {
		if f.fn(item) {
			filteredItems = append(filteredItems, item)
		}
	}
	return filteredItems
}

func WithSortBy(field string) IFilter {
	return sortByFilter{field: field}
}

func WithSortOrderAsc(ascending bool) sortOrderFilter {
	return sortOrderFilter{ascending: ascending}
}

func WithFilterFunc(fn func(item *models.Item) bool) IFilter {
	return filterFuncFilter{fn: fn}
}
