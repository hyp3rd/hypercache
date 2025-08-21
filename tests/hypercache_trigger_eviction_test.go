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
    hc, err := hypercache.NewInMemoryWithDefaults(1)
    assert.Nil(t, err)
    defer hc.Stop()

    // Set eviction interval to zero; eviction loop will run on manual trigger
    hypercache.ApplyHyperCacheOptions(hc, hypercache.WithEvictionInterval[backend.InMemory](0))

    // Add two items beyond capacity to force eviction need
    assert.Nil(t, hc.Set(context.TODO(), "k1", "v1", 0))
    assert.Nil(t, hc.Set(context.TODO(), "k2", "v2", 0))

    // Without waiting, trigger eviction explicitly (non-blocking)
    // Rapid fire triggers should be non-blocking
    for i := 0; i < 5; i++ {
        hc.TriggerEviction()
    }

    // Allow a tiny time for worker to process
    time.Sleep(50 * time.Millisecond)

    // Eventually item count should be <= capacity (1)
    assert.True(t, hc.Count(context.TODO()) <= 1)
}
