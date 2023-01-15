package backend

import (
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/hyp3rd/hypercache/cache"
)

// SortFilters holds the filters applied when listing the items in the cache
type SortFilters struct {
	// SortBy is the field to sort the items by.
	// The field can be any of the fields in the `Item` struct.
	SortBy string
	// SortAscending is a boolean indicating whether the items should be sorted in ascending order.
	// If set to false, the items will be sorted in descending order.
	SortAscending bool
	// FilterFunc is a predicate that takes a `Item` as an argument and returns a boolean indicating whether the item should be included in the cache.
	FilterFunc func(item *cache.Item) bool // filters applied when listing the items in the cache
}

type itemSorterByKey struct {
	items []*cache.Item
}

func (s *itemSorterByKey) Len() int           { return len(s.items) }
func (s *itemSorterByKey) Swap(i, j int)      { s.items[i], s.items[j] = s.items[j], s.items[i] }
func (s *itemSorterByKey) Less(i, j int) bool { return s.items[i].Key < s.items[j].Key }

type itemSorterByValue struct {
	items []*cache.Item
}

// func (s *itemSorterByValue) Len() int      { return len(s.items) }
// func (s *itemSorterByValue) Swap(i, j int) { s.items[i], s.items[j] = s.items[j], s.items[i] }
// func (s *itemSorterByValue) Less(i, j int) bool {
// 	return s.cmp.Equal(s.items[i].Value, s.items[j].Value)
// }

func (s *itemSorterByValue) Len() int      { return len(s.items) }
func (s *itemSorterByValue) Swap(i, j int) { s.items[i], s.items[j] = s.items[j], s.items[i] }

// func (s *itemSorterByValue) Less(i, j int) bool { return cmp.Equal(s.items[i].Value, s.items[j].Value) }
func (s *itemSorterByValue) Less(i, j int) bool {
	return cmp.Equal(s.items[i].Value, s.items[j].Value,
		cmpopts.IgnoreUnexported(),
		cmpopts.EquateEmpty(),
		cmpopts.SortSlices(func(x, y interface{}) bool {
			return x.(string) < y.(string)
		},
		),
		cmpopts.SortMaps(func(x, y interface{}) bool {
			return x.(string) < y.(string)
		},
		),
		cmpopts.SortSlices(func(x, y interface{}) bool {
			return x.(int) < y.(int)
		},
		),
		cmpopts.SortSlices(func(x, y interface{}) bool {
			return x.(string) < y.(string)
		},
		),
		cmpopts.SortMaps(func(x, y interface{}) bool {
			return x.(int) < y.(int)
		},
		),
		cmpopts.SortSlices(func(x, y interface{}) bool {
			return x.(float64) < y.(float64)
		},
		),
		cmpopts.SortMaps(func(x, y interface{}) bool {
			return x.(float64) < y.(float64)
		},
		),
	)
}

type itemSorterByExpiration struct {
	items []*cache.Item
}

func (s *itemSorterByExpiration) Len() int      { return len(s.items) }
func (s *itemSorterByExpiration) Swap(i, j int) { s.items[i], s.items[j] = s.items[j], s.items[i] }
func (s *itemSorterByExpiration) Less(i, j int) bool {
	return s.items[i].Expiration < s.items[j].Expiration
}

type itemSorterByLastAccess struct {
	items []*cache.Item
}

func (s *itemSorterByLastAccess) Len() int      { return len(s.items) }
func (s *itemSorterByLastAccess) Swap(i, j int) { s.items[i], s.items[j] = s.items[j], s.items[i] }
func (s *itemSorterByLastAccess) Less(i, j int) bool {
	return s.items[i].LastAccess.UnixNano() < s.items[j].LastAccess.UnixNano()
}

type itemSorterByAccessCount struct {
	items []*cache.Item
}

func (s *itemSorterByAccessCount) Len() int      { return len(s.items) }
func (s *itemSorterByAccessCount) Swap(i, j int) { s.items[i], s.items[j] = s.items[j], s.items[i] }
func (s *itemSorterByAccessCount) Less(i, j int) bool {
	return s.items[i].AccessCount < s.items[j].AccessCount
}
