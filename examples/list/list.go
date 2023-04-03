package main

import (
	"context"
	"fmt"
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/backend"
	"github.com/hyp3rd/hypercache/types"
)

// This example demonstrates how to list items from the cache
func main() {
	// Create a new HyperCache with a capacity of 400
	hyperCache, err := hypercache.NewInMemoryWithDefaults(400)

	if err != nil {
		fmt.Println(err)
		return
	}
	// Stop the cache when the program exits
	defer hyperCache.Stop()

	// Add 100 items to the cache
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("%d", i)
		val := fmt.Sprintf("val%d", i)

		err = hyperCache.Set(key, val, time.Minute)

		if err != nil {
			fmt.Printf("unexpected error: %v\n", err)
			return
		}
	}

	// Retrieve the list of items from the cache
	items, err := hyperCache.List(context.TODO())
	if err != nil {
		fmt.Println(err)
		return
	}

	// Apply filters
	// Define a filter function
	itemsFilterFunc := func(item *types.Item) bool {
		// return time.Since(item.LastAccess) > 1*time.Microsecond
		return item.Value != "val84"
	}

	sortByFilter := backend.WithSortBy(types.SortByKey.String())
	// sortOrderFilter := backend.WithSortOrderAsc(true)

	// Create a filterFuncFilter with the defined filter function
	filter := backend.WithFilterFunc(itemsFilterFunc)

	// Apply the filter to the items
	filteredItems := filter.ApplyFilter("in-memory", items)

	filteredItems = sortByFilter.ApplyFilter("in-memory", filteredItems)
	// sortedItems := sortOrderFilter.ApplyFilter("in-memory", filteredItems)

	for _, item := range filteredItems {
		fmt.Println(item.Key, item.Value)
	}
}
