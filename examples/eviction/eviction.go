package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/backend"
	"github.com/hyp3rd/hypercache/types"
)

// This example demonstrates how to setup eviction of items from the cache
func main() {
	log.Println("running an example of eviction with a background 3 seconds interval")
	executeExample(3 * time.Second)

	log.Println("running an example with background eviction disabled and proactive eviction enabled")
	executeExample(0)
}

// executeExample runs the example
func executeExample(evictionInterval time.Duration) {
	// Create a new HyperCache with a capacity of 10
	config := hypercache.NewConfig[backend.InMemory]("in-memory")
	config.HyperCacheOptions = []hypercache.Option[backend.InMemory]{
		hypercache.WithEvictionInterval[backend.InMemory](evictionInterval),
	}

	config.InMemoryOptions = []backend.Option[backend.InMemory]{
		backend.WithCapacity[backend.InMemory](10),
	}

	// Create a new HyperCache with a capacity of 10
	cache, err := hypercache.New(config)
	if err != nil {
		fmt.Println(err)
		return
	}

	// Close the cache when the program exits
	defer cache.Stop()

	log.Println("cache capacity", cache.Capacity())

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
	list, err := cache.List(context.TODO(), backend.WithSortBy[backend.InMemory](types.SortByKey))
	if err != nil {
		fmt.Println(err)
		return
	}

	// Print the list of items
	for i, ci := range list {
		fmt.Println(i, ci.Value)
	}

	if evictionInterval > 0 {
		fmt.Println("sleeping to allow the evition loop to complete", evictionInterval+2*time.Second)
		time.Sleep(evictionInterval + 2*time.Second)
		log.Println("listing all items in the cache the eviction is triggered")
		list, err = cache.List(context.TODO(), backend.WithSortBy[backend.InMemory](types.SortByKey))
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
