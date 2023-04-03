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

		err = hyperCache.Set(context.TODO(), key, val, time.Minute)
		time.Sleep(time.Millisecond * 350)
		if err != nil {
			fmt.Printf("unexpected error: %v\n", err)
			return
		}
	}

	// Apply filters
	// Define a filter function
	itemsFilterFunc := func(item *types.Item) bool {
		// return time.Since(item.LastAccess) > 1*time.Microsecond
		return item.Value != "val8"
	}

	sortByFilter := backend.WithSortBy(types.SortByExpiration.String())
	sortOrderFilter := backend.WithSortOrderAsc(true)

	// Create a filterFuncFilter with the defined filter function
	filter := backend.WithFilterFunc(itemsFilterFunc)

	// Retrieve the list of items from the cache
	items, err := hyperCache.List(context.TODO(), sortByFilter, sortOrderFilter, filter)
	if err != nil {
		fmt.Println(err)
		return
	}

	for _, item := range items {
		fmt.Println(item.Key, item.Value)
	}
}
