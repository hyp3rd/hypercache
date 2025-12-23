package tests

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/pkg/backend"
)

func BenchmarkHyperCache_Set(b *testing.B) {
	// Create a new HyperCache with a capacity of 100000
	cache, _ := hypercache.NewInMemoryWithDefaults(context.TODO(), 100000)

	b.ResetTimer()

	for i := range b.N {
		// Store a value in the cache with a key and expiration duration
		cache.Set(context.TODO(), fmt.Sprintf("key-%d", i), "value", time.Hour)
	}
}

func BenchmarkHyperCache_Set_Proactive_Eviction(b *testing.B) {
	// Create a new HyperCache with a capacity of 100000
	config := hypercache.NewConfig[backend.InMemory](constants.InMemoryBackend)

	config.HyperCacheOptions = []hypercache.Option[backend.InMemory]{
		hypercache.WithEvictionInterval[backend.InMemory](0),
		hypercache.WithEvictionAlgorithm[backend.InMemory]("lru"),
	}

	config.InMemoryOptions = []backend.Option[backend.InMemory]{
		backend.WithCapacity[backend.InMemory](100000),
	}

	// Create a new HyperCache with a capacity of 10
	cache, _ := hypercache.New(context.TODO(), hypercache.GetDefaultManager(), config)

	for i := 0; b.Loop(); i++ {
		// Store a value in the cache with a key and expiration duration
		cache.Set(context.TODO(), fmt.Sprintf("key-%d", i), "value", time.Hour)
	}
}
