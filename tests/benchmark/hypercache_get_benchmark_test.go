package tests

import (
	"context"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/backend"
	"github.com/hyp3rd/hypercache/internal/constants"
)

func BenchmarkHyperCache_Get(b *testing.B) {
	// Create a new HyperCache with a capacity of 1000
	cache, _ := hypercache.NewInMemoryWithDefaults(1000)

	// Store a value in the cache with a key and expiration duration
	cache.Set(context.TODO(), "key", "value", time.Hour)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Retrieve the value from the cache using the key
		cache.Get(context.TODO(), "key")
	}
}

func BenchmarkHyperCache_Get_ProactiveEviction(b *testing.B) {
	// Create a new HyperCache with a capacity of 1000
	config := hypercache.NewConfig[backend.InMemory](constants.InMemoryBackend)
	config.HyperCacheOptions = []hypercache.Option[backend.InMemory]{
		hypercache.WithEvictionInterval[backend.InMemory](0),
		hypercache.WithEvictionAlgorithm[backend.InMemory]("lru"),
	}

	config.InMemoryOptions = []backend.Option[backend.InMemory]{
		backend.WithCapacity[backend.InMemory](1000),
	}

	// Create a new HyperCache with a capacity of 10
	cache, _ := hypercache.New(hypercache.GetDefaultManager(), config)

	// Store a value in the cache with a key and expiration duration
	cache.Set(context.TODO(), "key", "value", time.Hour)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Retrieve the value from the cache using the key
		cache.Get(context.TODO(), "key")
	}
}
