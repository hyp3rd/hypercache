package tests

import (
	"testing"
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/backend"
)

func BenchmarkHyperCache_Get(b *testing.B) {
	// Create a new HyperCache with a capacity of 1000
	// cache, _ := hypercache.New(1000, hypercache.WithEvictionInterval[backend.InMemory](30*time.Second))
	cache, _ := hypercache.NewInMemoryWithDefaults(1000)

	// Store a value in the cache with a key and expiration duration
	cache.Set("key", "value", time.Hour)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Retrieve the value from the cache using the key
		_, _ = cache.Get("key")
	}
}

func BenchmarkHyperCache_Get_ProactiveEviction(b *testing.B) {
	// Create a new HyperCache with a capacity of 1000
	config := hypercache.NewConfig[backend.InMemory]()
	config.HyperCacheOptions = []hypercache.Option[backend.InMemory]{
		hypercache.WithEvictionInterval[backend.InMemory](0),
		hypercache.WithEvictionAlgorithm[backend.InMemory]("lru"),
	}

	config.InMemoryOptions = []backend.Option[backend.InMemory]{
		backend.WithCapacity(1000),
	}

	// Create a new HyperCache with a capacity of 10
	cache, _ := hypercache.New(config)

	// Store a value in the cache with a key and expiration duration
	cache.Set("key", "value", time.Hour)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Retrieve the value from the cache using the key
		_, _ = cache.Get("key")
	}
}
