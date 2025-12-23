package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

const (
	cacheCapacity = 400
	items         = 100
)

// This example demonstrates how to list items from the cache.
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), constants.DefaultTimeout)
	defer cancel()
	// Create a new HyperCache with a capacity of 400
	hyperCache, err := hypercache.NewInMemoryWithDefaults(ctx, cacheCapacity)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return
	}
	// Stop the cache when the program exits
	defer hyperCache.Stop(ctx)

	// Add 100 items to the cache
	for i := range items {
		key := strconv.Itoa(i)
		val := fmt.Sprintf("val%d", i)

		err = hyperCache.Set(ctx, key, val, time.Minute)
		if err != nil {
			fmt.Fprintf(os.Stdout, "unexpected error: %v\n", err)

			return
		}
	}

	// Apply filters
	// Define a filter function
	itemsFilterFunc := func(item *cache.Item) bool {
		// return time.Since(item.LastAccess) > 1*time.Microsecond
		return item.Value != "val8"
	}

	sortByFilter := backend.WithSortBy(constants.SortByExpiration.String())

	sortOrder := backend.WithSortOrderAsc(true)

	// Create a filterFuncFilter with the defined filter function
	filter := backend.WithFilterFunc(itemsFilterFunc)

	// Retrieve the list of items from the cache
	items, err := hyperCache.List(ctx, sortByFilter, sortOrder, filter)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return
	}

	for _, item := range items {
		fmt.Fprintln(os.Stdout, item.Key, item.Value)
	}
}
