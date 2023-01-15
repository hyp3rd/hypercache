package main

import (
	"fmt"
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/backend"
	"github.com/hyp3rd/hypercache/backend/redis"
	"github.com/hyp3rd/hypercache/types"
)

func main() {
	redisStore, err := redis.NewStore(
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

	value, ok := hyperCache.Get("key-100")

	if !ok {
		fmt.Println("key not found")
	}

	allItems, err := hyperCache.List(
		backend.WithSortBy[backend.RedisBackend](types.SortByValue),
		backend.WithSortOrderAsc[backend.RedisBackend](true),
	)
	if err != nil {
		panic(err)
	}

	for _, item := range allItems {
		fmt.Println(item.Key, item.Value)
	}

	fmt.Println(value)

}
