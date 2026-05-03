package tests

import (
	"context"
	"testing"
	"time"

	"github.com/longbridgeapp/assert"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/pkg/backend"
)

// Test TriggerEviction when evictionInterval == 0 triggers immediate eviction of overflow item(s).
func TestHyperCache_TriggerEviction_Immediate(t *testing.T) {
	hc, err := hypercache.NewInMemoryWithDefaults(context.TODO(), 1)
	assert.Nil(t, err)

	defer hc.Stop(context.TODO())

	// Set eviction interval to zero; eviction loop will run on manual trigger
	hypercache.ApplyHyperCacheOptions(hc, hypercache.WithEvictionInterval[backend.InMemory](0))

	// Add two items beyond capacity to force eviction need
	assert.Nil(t, hc.Set(context.TODO(), "k1", "v1", 0))
	assert.Nil(t, hc.Set(context.TODO(), "k2", "v2", 0))

	// Without waiting, trigger eviction explicitly (non-blocking)
	// Rapid fire triggers should be non-blocking
	for range 5 {
		hc.TriggerEviction(context.TODO())
	}

	// Eventually item count should be <= capacity (1). Poll instead of a
	// fixed sleep — under -race the eviction pipeline (channel send ->
	// expiration goroutine -> worker pool -> algorithm.Evict + Remove)
	// can take well over 50 ms.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hc.Count(context.TODO()) <= 1 {
			break
		}

		time.Sleep(20 * time.Millisecond)
	}

	assert.True(t, hc.Count(context.TODO()) <= 1)
}
