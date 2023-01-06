package tests

import (
	"fmt"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache"
)

func BenchmarkHyperCache_Set(b *testing.B) {
	// Create a new HyperCache with a capacity of 1000
	cache, _ := hypercache.NewHyperCache(1000, hypercache.WithEvictionInterval(30*time.Second))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Store a value in the cache with a key and expiration duration
		cache.Set(fmt.Sprintf("key-%d", i), "value", time.Hour)
	}
}

func BenchmarkHyperCache_Set_Proactive_Eviction(b *testing.B) {
	// Create a new HyperCache with a capacity of 1000
	cache, _ := hypercache.NewHyperCache(1000, hypercache.WithEvictionInterval(0))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Store a value in the cache with a key and expiration duration
		cache.Set(fmt.Sprintf("key-%d", i), "value", time.Hour)
	}
}
