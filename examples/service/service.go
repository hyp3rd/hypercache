package main

import (
	"fmt"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/middleware"
	"go.uber.org/zap"
)

func main() {
	var svc hypercache.HyperCacheService
	hyperCache, err := hypercache.NewHyperCacheInMemoryWithDefaults(10)

	if err != nil {
		fmt.Println(err)
		return
	}
	statsCollector := hyperCache.StatsCollector
	svc = hyperCache

	if err != nil {
		fmt.Println(err)
		return
	}

	// logger := logrus.New()
	// logger.SetFormatter(&logrus.JSONFormatter{})
	logger, _ := zap.NewProduction()

	sugar := logger.Sugar()
	defer sugar.Sync()
	defer logger.Sync()

	svc = hypercache.ApplyMiddleware(svc,
		// middleware.NewLoggingMiddleware,
		func(next hypercache.HyperCacheService) hypercache.HyperCacheService {
			return middleware.NewLoggingMiddleware(next, sugar)
		},
		func(next hypercache.HyperCacheService) hypercache.HyperCacheService {
			return middleware.NewStatsCollectorMiddleware(next, statsCollector)
		},
	)

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
}
