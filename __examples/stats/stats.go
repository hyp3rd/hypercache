package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/backend"
	"github.com/hyp3rd/hypercache/internal/constants"
)

const (
	cacheCapacity = 100
	delay         = 10 * time.Second
)

func main() {
	// Create a new HyperCache with a capacity of 100
	config := hypercache.NewConfig[backend.InMemory](constants.InMemoryBackend)
	config.HyperCacheOptions = []hypercache.Option[backend.InMemory]{
		hypercache.WithEvictionInterval[backend.InMemory](constants.DefaultEvictionInterval),
		hypercache.WithEvictionAlgorithm[backend.InMemory](constants.DefaultEvictionAlgorithm),
		hypercache.WithExpirationInterval[backend.InMemory](constants.DefaultExpirationInterval),
	}

	config.InMemoryOptions = []backend.Option[backend.InMemory]{
		backend.WithCapacity[backend.InMemory](cacheCapacity),
	}

	// Create a new HyperCache with a capacity of 10
	hyperCache, err := hypercache.New(hypercache.GetDefaultManager(), config)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return
	}
	// Stop the cache when the program exits
	defer hyperCache.Stop()

	fmt.Fprintln(os.Stdout, "Adding 300 items to the cache")
	// Add 300 items to the cache
	for i := range 300 {
		key := fmt.Sprintf("key%d", i)
		val := fmt.Sprintf("val%d", i)

		err = hyperCache.Set(context.TODO(), key, val, time.Minute)
		if err != nil {
			fmt.Fprintf(os.Stdout, "unexpected error: %v\n", err)

			return
		}
	}

	fmt.Fprintln(os.Stdout, "Sleeping to allow the cache to run its eviction cycle")
	time.Sleep(delay)

	// Retrieve the list of items from the cache
	list, err := hyperCache.List(context.TODO())
	// Check for errors
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return
	}

	fmt.Fprintf(os.Stdout, "Printing the list of items in the cache (should be %v items)\n\n", hyperCache.Capacity())
	// Print the list of items
	for i, ci := range list {
		fmt.Fprintln(os.Stdout, i, ci.Value)
	}

	fmt.Fprintf(os.Stdout, "\nDisplaying the stats for the cache (should be %v items) with 1 eviction cycle\n\n", hyperCache.Capacity())

	stats := hyperCache.GetStats()

	// iterate over the stats and print them
	for stat, statData := range stats {
		fmt.Fprintf(os.Stdout, "# Stat: %s\n", stat)
		fmt.Fprintf(os.Stdout, "Mean: %f\n", statData.Mean)
		fmt.Fprintf(os.Stdout, "Median: %f\n", statData.Median)
		fmt.Fprintf(os.Stdout, "Min: %d\n", statData.Min)
		fmt.Fprintf(os.Stdout, "Max: %d\n", statData.Max)
		fmt.Fprintf(os.Stdout, "Values: %v\n", statData.Values)
		fmt.Fprintf(os.Stdout, "Count: %d\n", statData.Count)
		fmt.Fprintf(os.Stdout, "Sum: %d\n", statData.Sum)
		fmt.Fprintf(os.Stdout, "Variance: %f\n\n", statData.Variance)
	}
}
