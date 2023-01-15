package main

import (
	"fmt"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/middleware"
	"go.uber.org/zap"
)

func main() {
	var svc hypercache.Service
	hyperCache, err := hypercache.NewInMemoryWithDefaults(10)
	defer hyperCache.Stop()

	if err != nil {
		fmt.Println(err)
		return
	}
	// assign statsCollector of the backend to use it in middleware
	statsCollector := hyperCache.StatsCollector
	svc = hyperCache

	if err != nil {
		fmt.Println(err)
		return
	}

	// Example of using zap logger from uber
	logger, _ := zap.NewProduction()

	sugar := logger.Sugar()
	defer sugar.Sync()
	defer logger.Sync()

	// apply middleware in the same order as you want to execute them
	svc = hypercache.ApplyMiddleware(svc,
		// middleware.YourMiddleware,
		func(next hypercache.Service) hypercache.Service {
			return middleware.NewLoggingMiddleware(next, sugar)
		},
		func(next hypercache.Service) hypercache.Service {
			return middleware.NewStatsCollectorMiddleware(next, statsCollector)
		},
	)
	defer svc.Stop()

	err = svc.Set("key string", "value any", 0)
	if err != nil {
		fmt.Println(err)
		return
	}
	key, ok := svc.Get("key string")
	if !ok {
		fmt.Println("key not found")
		return
	}
	fmt.Println(key)

	for i := 0; i < 10; i++ {
		svc.Set(fmt.Sprintf("key%v", i), fmt.Sprintf("val%v", i), 0)
	}

	items, errs := svc.GetMultiple("key1", "key7", "key9", "key9999")
	for k, e := range errs {
		fmt.Printf("error fetching item %s: %s\n", k, e)
	}

	for k, v := range items {
		fmt.Println(k, v)
	}

	val, err := svc.GetOrSet("key9999", "val9999", 0)
	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Println(val)

	svc.Remove("key9999", "key1")
}
