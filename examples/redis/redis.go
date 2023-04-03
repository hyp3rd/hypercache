package main

import (
	"context"
	"fmt"
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/backend"
	"github.com/hyp3rd/hypercache/backend/redis"
	"github.com/hyp3rd/hypercache/types"
)

func main() {
	redisStore, err := redis.New(
		redis.WithAddr("localhost:6379"),
		redis.WithPassword("k7oMs2G5bc4mRN45jPZjLBZxuMFrCLahvPn648Zwq1lT41gSYZqapBRnSF2L995FaYcZBz8c7xkKXku94HeReDgdwBu1N4CzgfQ94Z504hjfzrST1u0idVkbXe8ust"),
		redis.WithDB(0),
	)

	if err != nil {
		panic(err)
	}

	conf := &hypercache.Config[backend.Redis]{
		BackendType: "redis",
		RedisOptions: []backend.Option[backend.Redis]{
			backend.WithRedisClient(redisStore.Client),
			backend.WithCapacity[backend.Redis](20),
		},
		HyperCacheOptions: []hypercache.Option[backend.Redis]{
			hypercache.WithEvictionInterval[backend.Redis](time.Second * 5),
		},
	}

	hyperCache, err := hypercache.New(hypercache.GetDefaultManager(), conf)
	if err != nil {
		panic(err)
	}

	fmt.Println("setting 50 items to the cache")
	for i := 0; i < 50; i++ {
		err = hyperCache.Set(fmt.Sprintf("key-%d", i), fmt.Sprintf("value-%d", i), time.Hour)
		if err != nil {
			panic(err)
		}
	}

	fmt.Println("count", hyperCache.Count())
	fmt.Println("capacity", hyperCache.Capacity())

	fmt.Println("fetching all items (sorted by key, ascending, filtered by value != 'value-16')")

	// Retrieve the list of items from the cache
	allItems, err := hyperCache.List(context.TODO())
	if err != nil {
		fmt.Println(err)
		return
	}

	// Apply filters
	// Define a filter function
	itemsFilterFunc := func(item *types.Item) bool {
		// return time.Since(item.LastAccess) > 1*time.Microsecond
		return item.Value != "value-16"
	}

	sortByFilter := backend.WithSortBy(types.SortByKey.String())
	// sortOrderFilter := backend.WithSortOrderAsc(true)

	// Create a filterFuncFilter with the defined filter function
	filter := backend.WithFilterFunc(itemsFilterFunc)

	// Apply the filter to the items
	filteredItems := filter.ApplyFilter("redis", allItems)

	// Apply the sort filter to the filtered items
	filteredItems = sortByFilter.ApplyFilter("redis", filteredItems)

	fmt.Println("printing all items")
	// Print the list of items
	for _, item := range filteredItems {
		fmt.Println(item.Key, item.Value)
	}

	fmt.Println("count", hyperCache.Count())
	fmt.Println("capacity", hyperCache.Capacity())

	fmt.Println("sleep for 5 seconds to trigger eviction")
	time.Sleep(time.Second * 5)

	fmt.Println("fetching all items again")
	allItems, err = hyperCache.List(context.TODO())
	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Println("printing all items")
	// Print the list of items
	for _, item := range allItems {
		fmt.Println(item.Key, item.Value)
	}

	fmt.Println("count", hyperCache.Count())

	value, ok := hyperCache.Get("key-49")
	if ok {
		fmt.Println(value)
	}
}
