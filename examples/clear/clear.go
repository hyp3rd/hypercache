package main

import (
	"fmt"

	"github.com/hyp3rd/hypercache"
)

func main() {
	// Create a new HyperCache with a capacity of 10000
	cache, err := hypercache.NewHyperCacheInMemoryWithDefaults(10000)
	if err != nil {
		fmt.Println(err)
		return
	}
	// Stop the cache when the program exits
	defer cache.Stop()

	fmt.Println("adding 10000 items to cache")
	for i := 0; i < 10000; i++ {
		cache.Set("key", "value", 0)
	}

	fmt.Println("capacity", cache.Capacity())
	fmt.Println("size", cache.Size())
	fmt.Println("clearing cache")
	fmt.Println("capacity", cache.Capacity())
	fmt.Println("size", cache.Size())
}
