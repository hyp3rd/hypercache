package tests

import (
	"testing"
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/backend"
)

func BenchmarkHyperCache_Get(b *testing.B) {
	// Create a new HyperCache with a capacity of 1000
	// cache, _ := hypercache.NewHyperCache(1000, hypercache.WithEvictionInterval[backend.InMemoryBackend](30*time.Second))
	cache, _ := hypercache.NewHyperCacheInMemoryWithDefaults(1000)

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
	config := hypercache.NewConfig[backend.InMemoryBackend]()
	config.HyperCacheOptions = []hypercache.HyperCacheOption[backend.InMemoryBackend]{
		hypercache.WithEvictionInterval[backend.InMemoryBackend](0),
		hypercache.WithEvictionAlgorithm[backend.InMemoryBackend]("lru"),
	}

	config.InMemoryBackendOptions = []backend.BackendOption[backend.InMemoryBackend]{
		backend.WithCapacity(1000),
	}

	// Create a new HyperCache with a capacity of 10
	cache, _ := hypercache.NewHyperCache(config)

	// Store a value in the cache with a key and expiration duration
	cache.Set("key", "value", time.Hour)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Retrieve the value from the cache using the key
		_, _ = cache.Get("key")
	}
}
