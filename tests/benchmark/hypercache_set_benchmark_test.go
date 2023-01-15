package tests

import (
	"fmt"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/backend"
)

func BenchmarkHyperCache_Set(b *testing.B) {
	// Create a new HyperCache with a capacity of 1000
	cache, _ := hypercache.NewHyperCacheInMemoryWithDefaults(1000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Store a value in the cache with a key and expiration duration
		cache.Set(fmt.Sprintf("key-%d", i), "value", time.Hour)
	}
}

func BenchmarkHyperCache_Set_Proactive_Eviction(b *testing.B) {
	// Create a new HyperCache with a capacity of 1000
	config := hypercache.NewConfig[backend.InMemoryBackend]()
	config.HyperCacheOptions = []hypercache.Option[backend.InMemoryBackend]{
		hypercache.WithEvictionInterval[backend.InMemoryBackend](0),
		hypercache.WithEvictionAlgorithm[backend.InMemoryBackend]("cawolfu"),
	}

	config.InMemoryBackendOptions = []backend.BackendOption[backend.InMemoryBackend]{
		backend.WithCapacity(1000),
	}

	// Create a new HyperCache with a capacity of 10
	cache, _ := hypercache.NewHyperCache(config)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Store a value in the cache with a key and expiration duration
		cache.Set(fmt.Sprintf("key-%d", i), "value", time.Hour)
	}
}
