package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/pkg/middleware"
)

const (
	cacheCapacity = 10
)

func main() {
	var svc hypercache.Service

	ctx, cancel := context.WithTimeout(context.Background(), constants.DefaultTimeout*2)
	defer cancel()

	hyperCache, err := hypercache.NewInMemoryWithDefaults(ctx, cacheCapacity)
	defer hyperCache.Stop(ctx)

	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return
	}
	// assign statsCollector of the backend to use it in middleware
	statsCollector := hyperCache.StatsCollector
	svc = hyperCache

	// Example of using zap logger from uber
	logger := log.Default()

	// apply middleware in the same order as you want to execute them
	svc = hypercache.ApplyMiddleware(svc,
		// middleware.YourMiddleware,
		func(next hypercache.Service) hypercache.Service {
			return middleware.NewLoggingMiddleware(next, logger)
		},
		func(next hypercache.Service) hypercache.Service {
			return middleware.NewStatsCollectorMiddleware(next, statsCollector)
		},
	)

	err = svc.Set(ctx, "key string", "value any", 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return
	}

	key, ok := svc.Get(ctx, "key string")
	if !ok {
		fmt.Fprintln(os.Stdout, "key not found")

		return
	}

	fmt.Fprintln(os.Stdout, key)

	for i := range 10 {
		err := svc.Set(ctx, fmt.Sprintf("key%v", i), fmt.Sprintf("val%v", i), 0)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}

	items, errs := svc.GetMultiple(ctx, "key1", "key7", "key9", "key9999")
	for k, e := range errs {
		fmt.Fprintf(os.Stderr, "error fetching item %s: %s\n", k, e)
	}

	for k, v := range items {
		fmt.Fprintln(os.Stdout, k, v)
	}

	val, err := svc.GetOrSet(ctx, "key9999", "val9999", 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return
	}

	fmt.Fprintln(os.Stdout, val)

	err = svc.Remove(ctx, "key9999", "key1")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}

	// defer func() {
	// 	err := svc.Stop(ctx)
	// 	if err != nil {
	// 		fmt.Fprintln(os.Stderr, err)
	// 	}
	// }()
}
