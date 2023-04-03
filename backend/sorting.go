package backend

import "github.com/hyp3rd/hypercache/types"

// SortFilters holds the filters applied when listing the items in the cache
type SortFilters struct {
	// SortBy is the field to sort the items by.
	// The field can be any of the fields in the `Item` struct.
	SortBy string
	// SortAscending is a boolean indicating whether the items should be sorted in ascending order.
	// If set to false, the items will be sorted in descending order.
	SortAscending bool
	// FilterFunc is a predicate that takes a `Item` as an argument and returns a boolean indicating whether the item should be included in the cache.
	FilterFunc func(item *types.Item) bool // filters applied when listing the items in the cache
}

type itemSorterByKey struct {
	items []*types.Item
}

func (s *itemSorterByKey) Len() int           { return len(s.items) }
func (s *itemSorterByKey) Swap(i, j int)      { s.items[i], s.items[j] = s.items[j], s.items[i] }
func (s *itemSorterByKey) Less(i, j int) bool { return s.items[i].Key < s.items[j].Key }

type itemSorterByExpiration struct {
	items []*types.Item
}

func (s *itemSorterByExpiration) Len() int      { return len(s.items) }
func (s *itemSorterByExpiration) Swap(i, j int) { s.items[i], s.items[j] = s.items[j], s.items[i] }
func (s *itemSorterByExpiration) Less(i, j int) bool {
	return s.items[i].Expiration < s.items[j].Expiration
}

type itemSorterByLastAccess struct {
	items []*types.Item
}

func (s *itemSorterByLastAccess) Len() int      { return len(s.items) }
func (s *itemSorterByLastAccess) Swap(i, j int) { s.items[i], s.items[j] = s.items[j], s.items[i] }
func (s *itemSorterByLastAccess) Less(i, j int) bool {
	return s.items[i].LastAccess.UnixNano() < s.items[j].LastAccess.UnixNano()
}

type itemSorterByAccessCount struct {
	items []*types.Item
}

func (s *itemSorterByAccessCount) Len() int      { return len(s.items) }
func (s *itemSorterByAccessCount) Swap(i, j int) { s.items[i], s.items[j] = s.items[j], s.items[i] }
func (s *itemSorterByAccessCount) Less(i, j int) bool {
	return s.items[i].AccessCount < s.items[j].AccessCount
}
