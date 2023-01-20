package tests

import (
	"testing"
	"time"

	"github.com/hyp3rd/hypercache"
)

func BenchmarkHyperCache_List(b *testing.B) {
	// Create a new HyperCache with a capacity of 100000
	cache, _ := hypercache.NewInMemoryWithDefaults(100000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Store a value in the cache with a key and expiration duration
		cache.Set("key", "value", time.Hour)
	}

	list, _ := cache.List()

	for _, ci := range list {
		_ = ci
	}
}

// func BenchmarkHyperCache_List_ProactiveEviction(b *testing.B) {
// 	// Create a new HyperCache with a capacity of 1000
// 	cache, _ := hypercache.New(1000, hypercache.WithEvictionInterval[backend.InMemory](0))

// 	// Store a value in the cache with a key and expiration duration
// 	cache.Set("key", "value", time.Hour)

// 	b.ResetTimer()
// 	for i := 0; i < b.N; i++ {
// 		// Retrieve the value from the cache using the key
// 		_, _ = cache.Get("key")
// 	}
// }
