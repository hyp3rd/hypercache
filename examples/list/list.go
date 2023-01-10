package main

import (
	"fmt"
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/backend"
	"github.com/hyp3rd/hypercache/cache"
	"github.com/hyp3rd/hypercache/types"
)

// This example demonstrates how to list items from the cache
func main() {
	// Create a new HyperCache with a capacity of 100
	hyperCache, err := hypercache.NewHyperCache(200,
		hypercache.WithExpirationInterval(3*time.Second),
		hypercache.WithEvictionInterval(3*time.Second))

	if err != nil {
		fmt.Println(err)
		return
	}
	// Stop the cache when the program exits
	defer hyperCache.Stop()

	// Add 100 items to the cache
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("key%d", i)
		val := fmt.Sprintf("val%d", i)

		err = hyperCache.Set(key, val, time.Minute)

		if err != nil {
			fmt.Printf("unexpected error: %v\n", err)
			return
		}
	}

	// Retrieve the list of items from the cache
	list, err := hyperCache.List(
		backend.WithSortBy[backend.InMemoryBackend](types.SortByLastAccess),
		backend.WithSortDescending[backend.InMemoryBackend](),
		backend.WithFilterFunc[backend.InMemoryBackend](func(item *cache.CacheItem) bool {
			return item.Expiration > time.Second
		}),
	)

	// Check for errors
	if err != nil {
		fmt.Println(err)
		return
	}

	// Print the list of items
	for i, ci := range list {
		fmt.Println(i, ci.Value)
	}
}
