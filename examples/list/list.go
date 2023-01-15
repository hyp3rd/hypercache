package main

import (
	"fmt"
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/backend"
	"github.com/hyp3rd/hypercache/models"
	"github.com/hyp3rd/hypercache/types"
)

// This example demonstrates how to list items from the cache
func main() {
	// Create a new HyperCache with a capacity of 100
	hyperCache, err := hypercache.NewHyperCacheInMemoryWithDefaults(100)

	if err != nil {
		fmt.Println(err)
		return
	}
	// Stop the cache when the program exits
	defer hyperCache.Stop()

	// Add 100 items to the cache
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("key%d", i)
		val := i //fmt.Sprintf("%d", i)

		err = hyperCache.Set(key, val, time.Minute)

		if err != nil {
			fmt.Printf("unexpected error: %v\n", err)
			return
		}
	}

	// Retrieve the list of items from the cache
	list, err := hyperCache.List(
		backend.WithSortBy[backend.InMemoryBackend](types.SortByValue),
		backend.WithSortAscending[backend.InMemoryBackend](),
		backend.WithFilterFunc[backend.InMemoryBackend](func(item *models.Item) bool {
			return item.Value != "val98"
		}),
	)

	// Check for errors
	if err != nil {
		fmt.Println(err)
		return
	}

	// Print the list of items
	for _, ci := range list {
		fmt.Println(ci.Key, ci.Value)
	}
}
