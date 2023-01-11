package main

import (
	"fmt"
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/backend"
	"github.com/hyp3rd/hypercache/cache"
	"github.com/hyp3rd/hypercache/types"
)

func main() {
	// Create a new HyperCache with a capacity of 100
	hyperCache, err := hypercache.NewHyperCache(200,
		hypercache.WithExpirationInterval[backend.InMemoryBackend](3*time.Second),
		hypercache.WithEvictionInterval[backend.InMemoryBackend](3*time.Second))

	if err != nil {
		fmt.Println(err)
		return
	}
	// Stop the cache when the program exits
	defer hyperCache.Stop()

	fmt.Println("Adding 300 items to the cache")
	// Add 300 items to the cache
	for i := 0; i < 300; i++ {
		key := fmt.Sprintf("key%d", i)
		val := fmt.Sprintf("val%d", i)

		err = hyperCache.Set(key, val, time.Minute)

		if err != nil {
			fmt.Printf("unexpected error: %v\n", err)
			return
		}
	}

	fmt.Println("Sleeping for 5 seconds to allow the cache to run its eviction cycle")
	time.Sleep(time.Second * 5)

	// Retrieve the list of items from the cache
	list, err := hyperCache.List(
		backend.WithSortBy[backend.InMemoryBackend](types.SortByValue),
		backend.WithSortDescending[backend.InMemoryBackend](),
		backend.WithFilterFunc[backend.InMemoryBackend](func(item *cache.CacheItem) bool {
			return item.Expiration > time.Second
		}),
	)

	//
	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Printf("Printing the list of items in the cache (should be %v items)\n\n", hyperCache.Capacity())
	// Print the list of items
	for i, ci := range list {
		fmt.Println(i, ci.Value)
	}

	fmt.Printf("\nDisplaying the stats for the cache (should be %v items) with 1 eviction cycle\n\n", hyperCache.Capacity())

	stats := hyperCache.GetStats()

	// iterate over the stats and print them
	for stat, s := range stats {
		fmt.Printf("# Stat: %s\n", stat)
		fmt.Printf("Mean: %f\n", s.Mean)
		fmt.Printf("Median: %f\n", s.Median)
		fmt.Printf("Min: %d\n", s.Min)
		fmt.Printf("Max: %d\n", s.Max)
		fmt.Printf("Values: %v\n", s.Values)
		fmt.Printf("Count: %d\n", s.Count)
		fmt.Printf("Sum: %d\n", s.Sum)
		fmt.Printf("Variance: %f\n\n", s.Variance)
	}
}
