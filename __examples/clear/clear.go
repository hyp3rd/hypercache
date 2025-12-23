package main

import (
	"context"
	"fmt"
	"os"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/internal/constants"
)

const cacheCapacity = 100000

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), constants.DefaultTimeout)
	defer cancel()
	// Create a new HyperCache with a capacity of 100000
	cache, err := hypercache.NewInMemoryWithDefaults(ctx, cacheCapacity)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return
	}
	// Stop the cache when the program exits
	defer cache.Stop(ctx)

	fmt.Fprintln(os.Stdout, "adding 100000 items to cache")

	for i := range cacheCapacity {
		err := cache.Set(ctx, fmt.Sprintf("key%d", i), fmt.Sprintf("value%d", i), 0)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}

	item, ok := cache.Get(ctx, "key100")
	if ok {
		fmt.Fprintln(os.Stdout, "key100", item)
	}

	fmt.Fprintln(os.Stdout, "capacity", cache.Capacity())
	fmt.Fprintln(os.Stdout, "count", cache.Count(ctx))
	fmt.Fprintln(os.Stdout, "allocation", cache.Allocation())
	fmt.Fprintln(os.Stdout, "clearing cache")

	err = cache.Clear(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}

	fmt.Fprintln(os.Stdout, "capacity", cache.Capacity())
	fmt.Fprintln(os.Stdout, "count", cache.Count(ctx))
	fmt.Fprintln(os.Stdout, "allocation", cache.Allocation())
}
