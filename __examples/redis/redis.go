package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/pkg/backend"
	"github.com/hyp3rd/hypercache/pkg/backend/redis"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

const (
	cacheCapacity = 20
	items         = 50
	delay         = 5 * time.Second
)

//nolint:funlen
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), constants.DefaultTimeout*2)
	defer cancel()
	// Create a new Redis backend
	redisStore, err := redis.New(
		redis.WithAddr("localhost:6379"),
		redis.WithPassword("k7oMs2G5bc4mRN45jPZjLBZxuMFrCLahvPn648Zwq1lT41gSYZqapBRnSF2L995FaYcZBz8c7xkKXku94HeReDgdwBu1N4CzgfQ94Z504hjfzrST1u0idVkbXe8ust"),
		redis.WithDB(0),
	)
	if err != nil {
		panic(err)
	}

	conf := &hypercache.Config[backend.Redis]{
		BackendType: constants.RedisBackend,
		RedisOptions: []backend.Option[backend.Redis]{
			backend.WithRedisClient(redisStore.Client),
			backend.WithCapacity[backend.Redis](cacheCapacity),
		},
		HyperCacheOptions: []hypercache.Option[backend.Redis]{
			hypercache.WithEvictionInterval[backend.Redis](constants.DefaultEvictionInterval),
		},
	}

	hyperCache, err := hypercache.New(ctx, hypercache.GetDefaultManager(), conf)
	if err != nil {
		panic(err)
	}

	fmt.Fprintln(os.Stdout, "setting 50 items to the cache")

	for i := range items {
		err = hyperCache.Set(ctx, fmt.Sprintf("key-%d", i), fmt.Sprintf("value-%d", i), time.Hour)
		if err != nil {
			panic(err)
		}
	}

	fmt.Fprintln(os.Stdout, "count", hyperCache.Count(ctx))
	fmt.Fprintln(os.Stdout, "capacity", hyperCache.Capacity())

	fmt.Fprintln(os.Stdout, "fetching all items (sorted by key, ascending, filtered by value != 'value-16')")

	// Apply filters
	// Define a filter function
	itemsFilterFunc := func(item *cache.Item) bool {
		// return time.Since(item.LastAccess) > 1*time.Microsecond
		return item.Value != "value-16"
	}

	sortByFilter := backend.WithSortBy(constants.SortByKey.String())
	// sortOrderFilter := backend.WithSortOrderAsc(true)

	// Create a filterFuncFilter with the defined filter function
	filter := backend.WithFilterFunc(itemsFilterFunc)

	// Retrieve the list of items from the cache
	allItems, err := hyperCache.List(ctx, sortByFilter, filter)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return
	}

	fmt.Fprintln(os.Stdout, "printing all items")
	// Print the list of items
	for _, item := range allItems {
		fmt.Fprintln(os.Stdout, item.Key, item.Value)
	}

	fmt.Fprintln(os.Stdout, "count", hyperCache.Count(ctx))
	fmt.Fprintln(os.Stdout, "capacity", hyperCache.Capacity())

	fmt.Fprintln(os.Stdout, "sleep for 5 seconds to trigger eviction")

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-timer.C:
	case <-ctx.Done():
		fmt.Fprintln(os.Stdout, "context canceled before eviction")

		return
	}

	fmt.Fprintln(os.Stdout, "fetching all items again")

	allItems, err = hyperCache.List(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return
	}

	fmt.Fprintln(os.Stdout, "printing all items")
	// Print the list of items
	for _, item := range allItems {
		fmt.Fprintln(os.Stdout, item.Key, item.Value)
	}

	fmt.Fprintln(os.Stdout, "count", hyperCache.Count(ctx))

	value, ok := hyperCache.Get(ctx, "key-49")
	if ok {
		fmt.Fprintln(os.Stdout, value)
	}
}
