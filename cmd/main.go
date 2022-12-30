package main

import (
	"fmt"
	"os"
	"time"

	"github.com/hyp3rd/hypercache"
)

func main() {

	onItemExpire := func(key string, value interface{}) {
		// create the file
		f, err := os.Create("test.txt")
		if err != nil {
			fmt.Println(err)
		}
		// close the file with defer
		defer f.Close()
	}

	cache, err := hypercache.NewHyperCache(3, hypercache.WithExpirationInterval(3*time.Second), hypercache.WithEvictionInterval(3*time.Second))

	if err != nil {
		fmt.Println(err)
		return
	}

	cacheItem := hypercache.CacheItem{
		Key:           "NewKey",
		Value:         "hello, there",
		Duration:      2 * time.Second,
		OnItemExpired: onItemExpire,
	}
	err = cache.Add(cacheItem)
	if err != nil {
		fmt.Println(err)
		return
	}

	value, ok := cache.GetOrSet("keyz", "valuez", hypercache.WithDuration(5*time.Minute))
	if ok {
		fmt.Println("Value was retrieved from the cache:", value)
	} else {
		fmt.Println("Value was not found in the cache, so it was set:", value)
	}

	value, ok = cache.GetOrSet("keyz", "valuez")
	if ok {
		fmt.Println("Value was retrieved from the cache:", value)
	} else {
		fmt.Println("Value was not found in the cache, so it was set:", value)
	}

	// Try to retrieve the expired item from the cache
	_, ok = cache.Get(cacheItem.Key)
	if !ok {
		fmt.Println("item expired")
	}

	// Add an item to the cache with a key "key" and a value "value" that expires in 5 seconds
	err = cache.Set("key", "value", 5*time.Second)
	if err != nil {
		fmt.Println(err)
		return
	}

	// Retrieve the item from the cache
	value, ok = cache.Get("key")
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

	// time.Sleep(25 * time.Second)

	// Stop the expiration and eviction loops
	cache.Stop()
}
