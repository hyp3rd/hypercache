package main

import (
	"fmt"
	"log"
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/types"
)

// This example demonstrates how to setup eviction of items from the cache
func main() {
	log.Println("example of eviction with a 3 seconds interval")
	executeExample(5 * time.Second)

	log.Println("Example of background eviction disabled")
	executeExample(0)
}

// executeExample runs the example
func executeExample(evictionInterval time.Duration) {
	// Create a new HyperCache with a capacity of 10
	cache, err := hypercache.NewHyperCache(3,
		hypercache.EvictionAlgorithmName("cawolfu"),
		hypercache.WithEvictionInterval(evictionInterval),
		hypercache.WithMaxEvictionCount(10))
	if err != nil {
		fmt.Println(err)
		return
	}

	// Close the cache when the program exits
	defer cache.Stop()

	log.Println("initial capacity", cache.Capacity())

	log.Println("adding 15 items to the cache, 5 over capacity")
	for i := 0; i < 15; i++ {
		key := fmt.Sprintf("key%d", i)
		val := fmt.Sprintf("val%d", i)

		err = cache.Set(key, val, time.Minute)

		if err != nil {
			fmt.Printf("unexpected error: %v\n", err)
			return
		}
	}

	log.Println("capacity after adding 15 items", cache.Capacity())

	log.Println("listing all items in the cache")
	list, err := cache.List(hypercache.WithSortBy(types.SortByValue))
	if err != nil {
		fmt.Println(err)
		return
	}

	// Print the list of items
	for i, ci := range list {
		fmt.Println(i, ci.Value)
	}

	if evictionInterval > 0 {
		fmt.Println("sleeping for", evictionInterval+3*time.Second)
		time.Sleep(evictionInterval + 5*time.Second + 3*time.Second)
		log.Println("listing all items in the cache the eviction is triggered")
		list, err = cache.List(hypercache.WithSortBy(types.SortByValue))
		if err != nil {
			fmt.Println(err)
			return
		}
		// Print the list of items
		for i, ci := range list {
			fmt.Println(i, ci.Value)
		}
	}
}
