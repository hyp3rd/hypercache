package hypercache

import (
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/backend"
	"github.com/longbridgeapp/assert"
)

func TestHyperCache_NewHyperCache(t *testing.T) {
	// Test that an error is returned when the capacity is negative
	_, err := NewHyperCache[backend.InMemoryBackend](-1)
	if err == nil {
		t.Error("Expected an error when capacity is negative, got nil")
	}

	// Test that a new HyperCache is returned when the capacity is 0
	cache, err := NewHyperCache[backend.InMemoryBackend](0)
	if err != nil {
		t.Errorf("Unexpected error when capacity is 0: %v", err)
	}
	if cache == nil {
		t.Error("Expected a new HyperCache when capacity is 0, got nil")
	}

	// Test that a new HyperCache is returned when the capacity is positive
	cache, err = NewHyperCache[backend.InMemoryBackend](10)
	if err != nil {
		t.Errorf("Unexpected error when capacity is positive: %v", err)
	}
	if cache == nil {
		t.Error("Expected a new HyperCache when capacity is positive, got nil")
	}
}

func TestHyperCache_WithStatsCollector(t *testing.T) {
	// Test with default stats collector
	cache, err := NewHyperCache[backend.InMemoryBackend](10)
	assert.Nil(t, err)
	assert.NotNil(t, cache.statsCollector)
}

func TestHyperCache_WithExpirationInterval(t *testing.T) {
	// Test with default expiration interval
	cache, err := NewHyperCache[backend.InMemoryBackend](10)
	assert.Nil(t, err)
	assert.Equal(t, 10*time.Minute, cache.expirationInterval)

	// Test with custom expiration interval
	cache, err = NewHyperCache[backend.InMemoryBackend](10, WithExpirationInterval[backend.InMemoryBackend](1*time.Hour))
	assert.Nil(t, err)
	assert.Equal(t, 1*time.Hour, cache.expirationInterval)
}

func TestHyperCache_WithEvictionInterval(t *testing.T) {
	// Test with default eviction interval
	cache, err := NewHyperCache[backend.InMemoryBackend](10)
	assert.Nil(t, err)
	assert.Equal(t, 1*time.Minute, cache.evictionInterval)

	// Test with custom eviction interval
	cache, err = NewHyperCache[backend.InMemoryBackend](10, WithEvictionInterval[backend.InMemoryBackend](1*time.Hour))
	assert.Nil(t, err)
	assert.Equal(t, 1*time.Hour, cache.evictionInterval)
}

func TestHyperCache_WithMaxEvictionCount(t *testing.T) {
	// Test with default max eviction count
	cache, err := NewHyperCache[backend.InMemoryBackend](10)
	assert.Nil(t, err)
	assert.Equal(t, uint(10), cache.maxEvictionCount)

	// Test with custom max eviction count
	cache, err = NewHyperCache[backend.InMemoryBackend](10, WithMaxEvictionCount[backend.InMemoryBackend](5))
	assert.Nil(t, err)
	assert.Equal(t, uint(5), cache.maxEvictionCount)
}

// func TestHyperCache_WithEvictionTriggerBufferSize(t *testing.T) {
// 	// Test with eviction trigger buffer size of 0
// 	cache, err := NewHyperCache(10, WithEvictionTriggerBufferSize(0))
// 	assert.Nil(t, err)
// 	// check if the default value is used
// 	assert.Equal(t, uint(100), cache.evictionTriggerBufferSize)

// 	// Test with eviction trigger buffer size of 100
// 	cache, err = NewHyperCache(10, WithEvictionTriggerBufferSize(200))
// 	assert.Nil(t, err)
// 	assert.Equal(t, uint(200), cache.evictionTriggerBufferSize)
// }
