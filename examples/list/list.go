package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/backend"
	"github.com/hyp3rd/hypercache/types"
)

const (
	cacheCapacity = 400
	items         = 100
	delay         = time.Millisecond * 350
)

// This example demonstrates how to list items from the cache.
func main() {
	// Create a new HyperCache with a capacity of 400
	hyperCache, err := hypercache.NewInMemoryWithDefaults(cacheCapacity)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return
	}
	// Stop the cache when the program exits
	defer hyperCache.Stop()

	// Add 100 items to the cache
	for i := range items {
		key := strconv.Itoa(i)
		val := fmt.Sprintf("val%d", i)

		err = hyperCache.Set(context.TODO(), key, val, time.Minute)
		time.Sleep(delay)

		if err != nil {
			fmt.Fprintf(os.Stdout, "unexpected error: %v\n", err)

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

	sortOrder := backend.WithSortOrderAsc(true)

	// Create a filterFuncFilter with the defined filter function
	filter := backend.WithFilterFunc(itemsFilterFunc)

	// Retrieve the list of items from the cache
	items, err := hyperCache.List(context.TODO(), sortByFilter, sortOrder, filter)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return
	}

	for _, item := range items {
		fmt.Fprintln(os.Stdout, item.Key, item.Value)
	}
}
