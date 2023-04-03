package main

import (
	"context"
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

	fmt.Println("adding 100000 items to cache")
	for i := 0; i < 100000; i++ {
		cache.Set(context.TODO(), fmt.Sprintf("key%d", i), fmt.Sprintf("value%d", i), 0)
	}

	item, ok := cache.Get("key100")
	if ok {
		fmt.Println("key100", item)
	}

	fmt.Println("capacity", cache.Capacity())
	fmt.Println("count", cache.Count())
	fmt.Println("allocation", cache.Allocation())
	fmt.Println("clearing cache")
	cache.Clear(context.TODO())
	fmt.Println("capacity", cache.Capacity())
	fmt.Println("count", cache.Count())
	fmt.Println("allocation", cache.Allocation())
}
