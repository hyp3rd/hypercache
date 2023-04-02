package tests

import (
	"fmt"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/backend"
)

func BenchmarkHyperCache_Set(b *testing.B) {
	// Create a new HyperCache with a capacity of 100000
	cache, _ := hypercache.NewInMemoryWithDefaults(100000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Store a value in the cache with a key and expiration duration
		cache.Set(fmt.Sprintf("key-%d", i), "value", time.Hour)
	}
}

func BenchmarkHyperCache_Set_Proactive_Eviction(b *testing.B) {
	// Create a new HyperCache with a capacity of 100000
	config := hypercache.NewConfig[backend.InMemory]("in-memory")
	config.HyperCacheOptions = []hypercache.Option[backend.InMemory]{
		hypercache.WithEvictionInterval[backend.InMemory](0),
		hypercache.WithEvictionAlgorithm[backend.InMemory]("cawolfu"),
	}

	config.InMemoryOptions = []backend.Option[backend.InMemory]{
		backend.WithCapacity[backend.InMemory](100000),
	}

	// Create a new HyperCache with a capacity of 10
	cache, _ := hypercache.New(config)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Store a value in the cache with a key and expiration duration
		cache.Set(fmt.Sprintf("key-%d", i), "value", time.Hour)
	}
}
