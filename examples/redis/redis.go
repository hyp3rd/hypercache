package main

import (
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

	conf := &hypercache.Config[backend.RedisBackend]{
		RedisOptions: []backend.Option[backend.RedisBackend]{
			backend.WithRedisClient(redisStore.Client),
		},
	}

	hyperCache, err := hypercache.New(conf)
	if err != nil {
		panic(err)
	}

	for i := 0; i < 400; i++ {
		err = hyperCache.Set(fmt.Sprintf("key-%d", i), fmt.Sprintf("value-%d", i), time.Hour)
		if err != nil {
			panic(err)
		}
	}

	allItems, err := hyperCache.List(
		backend.WithSortBy[backend.RedisBackend](types.SortByKey),
		backend.WithSortOrderAsc[backend.RedisBackend](true),
		backend.WithFilterFunc[backend.RedisBackend](func(item *models.Item) bool {
			return item.Value != "value-210"
		}),
	)

	// Check for errors
	if err != nil {
		panic(err)
	}

	// Print the list of items
	for _, item := range allItems {
		fmt.Println(item.Key, item.Value)
	}

	// fmt.Println(value)
}
