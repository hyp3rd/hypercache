package main

import (
	"context"
	"fmt"
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/backend"
	"github.com/hyp3rd/hypercache/backend/redis"
	"github.com/hyp3rd/hypercache/models"
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
		RedisOptions: []backend.Option[backend.Redis]{
			backend.WithRedisClient(redisStore.Client),
			backend.WithCapacity[backend.Redis](20),
		},
		HyperCacheOptions: []hypercache.Option[backend.Redis]{
			hypercache.WithEvictionInterval[backend.Redis](time.Second * 5),
			hypercache.WithEvictionAlgorithm[backend.Redis]("clock"),
		},
	}

	hyperCache, err := hypercache.New(conf)
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
	allItems, err := hyperCache.List(context.TODO(),
		backend.WithSortBy[backend.Redis](types.SortByKey),
		backend.WithSortOrderAsc[backend.Redis](true),
		backend.WithFilterFunc[backend.Redis](func(item *models.Item) bool {
			return item.Value != "value-16"
		}),
	)

	// Check for errors
	if err != nil {
		fmt.Println(err)
	}

	fmt.Println("printing all items")
	// Print the list of items
	for _, item := range allItems {
		fmt.Println(item.Key, item.Value)
	}

	fmt.Println("count", hyperCache.Count())
	fmt.Println("capacity", hyperCache.Capacity())

	fmt.Println("sleep for 5 seconds to trigger eviction")
	time.Sleep(time.Second * 5)

	fmt.Println("fetching all items (sorted by key, ascending, filtered by value != 'value-16')")
	allItems, err = hyperCache.List(context.TODO(),
		backend.WithSortBy[backend.Redis](types.SortByKey),
		backend.WithSortOrderAsc[backend.Redis](true),
		backend.WithFilterFunc[backend.Redis](func(item *models.Item) bool {
			return item.Value != "value-16"
		}),
	)
	// Check for errors
	if err != nil {
		fmt.Println(err)
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
