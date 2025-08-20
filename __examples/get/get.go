package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/hyp3rd/hypercache"
)

const cacheCapacity = 10

func main() {
	// Create a new HyperCache with a capacity of 10
	cache, err := hypercache.NewInMemoryWithDefaults(cacheCapacity)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return
	}
	// Stop the cache when the program exits
	defer cache.Stop()

	log.Println("adding items to the cache")
	// Add 10 items to the cache
	for i := range 10 {
		key := fmt.Sprintf("key%d", i)
		val := fmt.Sprintf("val%d", i)

		err = cache.Set(context.TODO(), key, val, time.Minute)
		if err != nil {
			fmt.Fprintf(os.Stdout, "unexpected error: %v\n", err)

			return
		}
	}

	log.Println("fetching items from the cache using the `GetMultiple` method, key11 does not exist")
	// Retrieve the specific of items from the cache
	items, errs := cache.GetMultiple(context.TODO(), "key1", "key7", "key9", "key11")

	// Print the errors if any
	for k, e := range errs {
		log.Printf("error fetching item %s: %s\n", k, e)
	}

	// Print the items
	for k, v := range items {
		fmt.Fprintln(os.Stdout, k, v)
	}

	log.Println("fetching items from the cache using the `GetOrSet` method")
	// Retrieve a specific of item from the cache
	// If the item is not found, set it and return the value
	val, err := cache.GetOrSet(context.TODO(), "key11", "val11", time.Minute)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return
	}

	fmt.Fprintln(os.Stdout, val)

	log.Println("fetching items from the cache using the simple `Get` method")

	item, ok := cache.Get(context.TODO(), "key7")
	if !ok {
		fmt.Fprintln(os.Stdout, "item not found")

		return
	}

	fmt.Fprintln(os.Stdout, item)
}
