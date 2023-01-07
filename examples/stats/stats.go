package main

import (
	"fmt"
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/types"
)

func main() {
	// Create a new HyperCache with a capacity of 100
	cache, err := hypercache.NewHyperCache(200,
		hypercache.WithExpirationInterval(3*time.Second),
		hypercache.WithEvictionInterval(3*time.Second))

	if err != nil {
		fmt.Println(err)
		return
	}
	// Stop the cache when the program exits
	defer cache.Stop()

	fmt.Println("Adding 300 items to the cache")
	// Add 300 items to the cache
	for i := 0; i < 300; i++ {
		key := fmt.Sprintf("key%d", i)
		val := fmt.Sprintf("val%d", i)

		err = cache.Set(key, val, time.Minute)

		if err != nil {
			fmt.Printf("unexpected error: %v\n", err)
			return
		}
	}

	fmt.Println("Sleeping for 5 seconds to allow the cache to run its eviction cycle")
	time.Sleep(time.Second * 5)

	// Retrieve the list of items from the cache
	list, err := cache.List(
		hypercache.WithSortBy(types.SortByValue),
		hypercache.WithSortDescending(),
		hypercache.WithFilter(func(item *hypercache.CacheItem) bool {
			return item.Expiration > time.Second
		}),
	)

	//
	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Printf("Printing the list of items in the cache (should be %v items)\n\n", cache.Capacity())
	// Print the list of items
	for i, ci := range list {
		fmt.Println(i, ci.Value)
	}

	fmt.Printf("\nDisplaying the stats for the cache (should be %v items) with 1 eviction cycle\n\n", cache.Capacity())

	stats := cache.GetStats()

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
