package main

import (
	"fmt"
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/types"
)

func main() {

	// onItemExpire := func(key string, value interface{}) {
	// 	// create the file
	// 	f, err := os.Create("test.txt")
	// 	if err != nil {
	// 		fmt.Println(err)
	// 	}
	// 	// close the file with defer
	// 	defer f.Close()
	// }

	cache, err := hypercache.NewHyperCache(200, hypercache.WithExpirationInterval(3*time.Second), hypercache.WithEvictionInterval(3*time.Second))

	if err != nil {
		fmt.Println(err)
		return
	}

	// cacheItem := &hypercache.CacheItem{
	// 	Key:           "NewKey",
	// 	Value:         "hello, there",
	// 	Duration:      2 * time.Second,
	// 	OnItemExpired: onItemExpire,
	// }
	// err = cache.Add(cacheItem)
	// if err != nil {
	// 	fmt.Println(err)
	// 	return
	// }

	value, err := cache.GetOrSet("keyz", "valuez", time.Minute)
	if err != nil {
		fmt.Println("Value was not retrieved from the cache:", err)
	} else {
		fmt.Println("Value was not found in the cache", value)
	}

	value, err = cache.GetOrSet("keyz", "valuez", time.Minute)
	if err != nil {
		fmt.Println("Value was not retrieved from the cache:", err)
	} else {
		fmt.Println("Value was found in the cache:", value)
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

	// Wait for the item to expire
	time.Sleep(5 * time.Second)

	// Try to retrieve the expired item from the cache
	_, ok = cache.Get("key")
	if !ok {
		fmt.Println("item expired")
	}

	fmt.Println(cache.Capacity())

	err = cache.Set("key", "value", 5*time.Second)
	if err != nil {
		fmt.Println(err)
		return
	}

	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("key%d", i)
		val := fmt.Sprintf("val%d", i)

		err = cache.Set(key, val, time.Minute)

		if err != nil {
			fmt.Printf("unexpected error: %v\n", err)
		}
	}

	list, err := cache.List(
		hypercache.WithSortBy(types.SortByValue),
		hypercache.WithSortDescending(),
		hypercache.WithFilter(func(item *hypercache.CacheItem) bool {
			return item.Expiration > time.Second
		}),
	)

	if err != nil {
		fmt.Println(err)
	}

	for i, ci := range list {
		fmt.Println(i, ci.Value)
	}

	err = cache.Set("key10", "value", time.Minute)
	if err != nil {
		fmt.Println(err)
	}

	cache.SetCapacity(5)

	fmt.Println("capacity", cache.Capacity())
	fmt.Println("size", cache.Size())

	res, errs := cache.GetMultiple("key0", "key2", "key9")

	if errs != nil {
		fmt.Println(errs)
	}

	for k, v := range res {
		fmt.Println(k, v)
	}

	// time.Sleep(25 * time.Second)

	// Stop the expiration and eviction loops
	cache.Stop()
}
