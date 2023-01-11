package main

import (
	"fmt"
	"reflect"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/backend"
	"github.com/hyp3rd/hypercache/utils"
)

func main() {
	tr := utils.NewTypeRegistry()

	var x int = 3
	// Create a new HyperCache with a capacity of 10
	cache, err := hypercache.NewHyperCache[backend.InMemoryBackend](10)
	if err != nil {
		fmt.Println(err)
		return
	}
	// Stop the cache when the program exits
	defer cache.Stop()

	tr.CacheObject(x)
	tr.CacheObject(cache)

	fmt.Println("Registry:")

	fmt.Printf("Is int type cached: %t\n", tr.IsCached(reflect.TypeOf(x)))
	fmt.Printf("Is hypercache type cached: %t\n", tr.IsCached(reflect.TypeOf(cache)))

	obj, t := tr.GetCachedObject(reflect.TypeOf(cache))
	fmt.Printf("Cached object: %v, type: %v\n", obj, t)
	fmt.Println("cached object type: ", reflect.TypeOf(obj))
	fmt.Println("")

	typ, inferred := utils.TypeName(cache)
	fmt.Println("Type: ", typ)
	fmt.Println("Inferred: ", inferred)
	fmt.Println("cached object type friendly name: ")

	tr.RemoveObject(reflect.TypeOf(x))

	tr.ClearRegistry()
}
