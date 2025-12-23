package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/pkg/backend"
)

const (
	cacheCapacity            = 10
	evictionInterval         = 10 * time.Second
	evictionIntervalSlippage = 65 * time.Second
)

// This example demonstrates how to setup eviction of items from the cache.
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), constants.DefaultTimeout)
	defer cancel()
	log.Println("running an example of eviction with a background 3 seconds interval")
	executeExample(ctx, evictionInterval)

	log.Println("running an example with background eviction disabled and proactive eviction enabled")
	executeExample(ctx, 0)
}

// executeExample runs the example.
func executeExample(ctx context.Context, evictionInterval time.Duration) {
	// Create a new HyperCache with a capacity of 10
	config := hypercache.NewConfig[backend.InMemory](constants.InMemoryBackend)
	config.HyperCacheOptions = []hypercache.Option[backend.InMemory]{
		hypercache.WithEvictionInterval[backend.InMemory](evictionInterval),
	}

	config.InMemoryOptions = []backend.Option[backend.InMemory]{
		backend.WithCapacity[backend.InMemory](cacheCapacity),
	}

	// Create a new HyperCache with a capacity of 10
	cache, err := hypercache.New(ctx, hypercache.GetDefaultManager(), config)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return
	}

	// Close the cache when the program exits
	defer cache.Stop(ctx)

	log.Println("cache capacity", cache.Capacity())

	log.Println("adding 15 items to the cache, 5 over capacity")

	for i := range 15 {
		key := fmt.Sprintf("key%d", i)
		val := fmt.Sprintf("val%d", i)

		err = cache.Set(ctx, key, val, time.Minute)
		if err != nil {
			fmt.Fprintf(os.Stdout, "unexpected error: %v\n", err)

			return
		}
	}

	log.Println("capacity after adding 15 items", cache.Capacity())

	log.Println("listing all items in the cache")

	// Apply filters
	sortByFilter := backend.WithSortBy(constants.SortByKey.String())

	items, err := cache.List(ctx, sortByFilter)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return
	}

	for _, item := range items {
		fmt.Fprintln(os.Stdout, item.Key, item.Value)
	}

	if evictionInterval > 0 {
		fmt.Fprintln(os.Stdout, "sleeping to allow the eviction loop to complete", evictionInterval+evictionIntervalSlippage)
		time.Sleep(evictionInterval + evictionIntervalSlippage)
		log.Println("listing all items in the cache the eviction is triggered")

		list, err := cache.List(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)

			return
		}
		// Print the list of items
		for i, ci := range list {
			fmt.Fprintln(os.Stdout, i, ci.Value)
		}
	}
}
