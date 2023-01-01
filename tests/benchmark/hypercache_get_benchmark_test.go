package tests

import (
	"testing"
	"time"

	"github.com/hyp3rd/hypercache"
)

func BenchmarkHyperCache_Get(b *testing.B) {
	// Create a new HyperCache with a capacity of 1000
	cache, _ := hypercache.NewHyperCache(1000)

	// Store a value in the cache with a key and expiration duration
	cache.Set("key", "value", time.Hour)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Retrieve the value from the cache using the key
		_, _ = cache.Get("key")
	}
}
