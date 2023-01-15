package main

import (
	"fmt"

	"github.com/hyp3rd/hypercache"
)

func main() {
	// Create a new HyperCache with a capacity of 10000
	cache, err := hypercache.NewInMemoryWithDefaults(100000)
	if err != nil {
		fmt.Println(err)
		return
	}
	// Stop the cache when the program exits
	defer cache.Stop()

	fmt.Println("adding 10000 items to cache")
	for i := 0; i < 100000; i++ {
		cache.Set(fmt.Sprintf("key%d", i), "value", 0)
	}

	fmt.Println("capacity", cache.Capacity())
	fmt.Println("size", cache.Size())
	fmt.Println("clearing cache")
	cache.Clear()
	fmt.Println("capacity", cache.Capacity())
	fmt.Println("size", cache.Size())
}
