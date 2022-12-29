package main

import (
	"fmt"
	"time"

	"github.com/hyp3rd/hypercache"
)

func main() {
	// Create a new cache with a capacity of 100 items
	cache, err := hypercache.NewHyperCache(5, time.Duration(5*time.Minute), time.Duration(1*time.Minute))
	if err != nil {
		fmt.Println(err)
		return
	}

	// Add an item to the cache with a key "key" and a value "value" that expires in 5 seconds
	err = cache.Set("key", "value", 5*time.Second)
	if err != nil {
		fmt.Println(err)
		return
	}

	// Retrieve the item from the cache
	value, ok := cache.Get("key")
	if !ok {
		fmt.Println("item not found")
		return
	}
	fmt.Println(value) // "value"

	// Try to retrieve the expired item from the cache
	_, ok = cache.Get("key")
	if !ok {
		fmt.Println("item expired")
	}

	// Try to retrieve the expired item from the cache
	_, ok = cache.Get("key")
	if !ok {
		fmt.Println("item expired")
	}

	// Wait for the item to expire
	time.Sleep(5 * time.Second)

	fmt.Println(cache.Capacity())

	// Try to retrieve the expired item from the cache
	_, ok = cache.Get("key")
	if !ok {
		fmt.Println("item expired")
	}

	stats := cache.Stats()

	fmt.Println(stats)

	err = cache.Set("key", "value", 5*time.Second)
	if err != nil {
		fmt.Println(err)
		return
	}

	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("key%d", i)
		// t.Log(key)
		err = cache.Set(key, "value", time.Minute)

		if err != nil {
			fmt.Printf("unexpected error: %v\n", err)
		}
	}

	err = cache.Set("key10", "value", time.Minute)
	if err != nil {
		fmt.Println(err)
	}

	time.Sleep(5 * time.Second)
	fmt.Println(stats)
	fmt.Println(cache.Capacity())

	// Stop the expiration and eviction loops
	cache.Stop()
}
