package main

import (
	"fmt"
	"log"
	"time"

	"github.com/hyp3rd/hypercache"
)

func main() {
	// Create a new HyperCache with a capacity of 10
	cache, err := hypercache.NewHyperCache(10)
	if err != nil {
		fmt.Println(err)
		return
	}
	// Close the cache when the program exits
	defer cache.Close()

	log.Println("adding items to the cache")
	// Add 10 items to the cache
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("key%d", i)
		val := fmt.Sprintf("val%d", i)

		err = cache.Set(key, val, time.Minute)

		if err != nil {
			fmt.Printf("unexpected error: %v\n", err)
		}
	}

	log.Println("fetching items from the cache using the `GetMultiple` method")
	// Retrieve the specific of items from the cache
	items, errs := cache.GetMultiple("key1", "key7", "key9", "key11")

	// Print the errors if any
	for k, e := range errs {
		log.Printf("error fetching item %s: %s\n", k, e)
	}

	// Print the items
	for k, v := range items {
		fmt.Println(k, v)
	}

	log.Println("fetching items from the cache using the `GetOrSet` method")
	// Retrieve a specific of item from the cache
	// If the item is not found, set it and return the value
	val, err := cache.GetOrSet("key11", "val11", time.Minute)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(val)

	log.Println("fetching items from the cache using the simple `Get` method")
	item, ok := cache.Get("key7")
	if !ok {
		fmt.Println("item not found")
		return
	}
	fmt.Println(item)
}
