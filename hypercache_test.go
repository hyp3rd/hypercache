package hypercache

import (
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/backend"
	"github.com/longbridgeapp/assert"
)

func TestHyperCache_New(t *testing.T) {
	// Test that an error is returned when the capacity is negative
	_, err := NewInMemoryWithDefaults(-1)
	if err == nil {
		t.Error("Expected an error when capacity is negative, got nil")
	}

	// Test that a new HyperCache is returned when the capacity is 0
	cache, err := NewInMemoryWithDefaults(0)
	if err != nil {
		t.Errorf("Unexpected error when capacity is 0: %v", err)
	}
	if cache == nil {
		t.Error("Expected a new HyperCache when capacity is 0, got nil")
	}

	// Test that a new HyperCache is returned when the capacity is positive
	cache, err = NewInMemoryWithDefaults(10)
	if err != nil {
		t.Errorf("Unexpected error when capacity is positive: %v", err)
	}
	if cache == nil {
		t.Error("Expected a new HyperCache when capacity is positive, got nil")
	}
}

func TestHyperCache_WithStatsCollector(t *testing.T) {
	// Test with default stats collector
	cache, err := NewInMemoryWithDefaults(10)
	assert.Nil(t, err)
	assert.NotNil(t, cache.StatsCollector)
}

func TestHyperCache_WithExpirationInterval(t *testing.T) {
	// Test with default expiration interval
	cache, err := NewInMemoryWithDefaults(10)
	assert.Nil(t, err)
	assert.Equal(t, 30*time.Minute, cache.expirationInterval)

	config := &Config[backend.InMemory]{
		HyperCacheOptions: []Option[backend.InMemory]{
			WithExpirationInterval[backend.InMemory](1 * time.Hour),
		},
		InMemoryOptions: []backend.Option[backend.InMemory]{
			backend.WithCapacity[backend.InMemory](10),
		},
	}
	// Test with custom expiration interval
	cache, err = New(config)
	assert.Nil(t, err)
	assert.Equal(t, 1*time.Hour, cache.expirationInterval)
}

func TestHyperCache_WithEvictionInterval(t *testing.T) {
	// Test with default eviction interval
	cache, err := NewInMemoryWithDefaults(10)
	assert.Nil(t, err)
	assert.Equal(t, 10*time.Minute, cache.evictionInterval)

	// Test with custom eviction interval
	config := &Config[backend.InMemory]{
		HyperCacheOptions: []Option[backend.InMemory]{
			WithEvictionInterval[backend.InMemory](1 * time.Hour),
		},
		InMemoryOptions: []backend.Option[backend.InMemory]{
			backend.WithCapacity[backend.InMemory](10),
		},
	}
	// Test with custom eviction interval
	cache, err = New(config)
	assert.Nil(t, err)
	assert.Equal(t, 1*time.Hour, cache.evictionInterval)
}

func TestHyperCache_WithMaxEvictionCount(t *testing.T) {
	// Test with default max eviction count
	cache, err := NewInMemoryWithDefaults(10)
	assert.Nil(t, err)
	assert.Equal(t, uint(10), cache.maxEvictionCount)

	// Test with custom max eviction count
	config := &Config[backend.InMemory]{
		HyperCacheOptions: []Option[backend.InMemory]{
			WithEvictionInterval[backend.InMemory](1 * time.Hour),
			WithMaxEvictionCount[backend.InMemory](5),
		},
		InMemoryOptions: []backend.Option[backend.InMemory]{
			backend.WithCapacity[backend.InMemory](10),
		},
	}
	cache, err = New(config)
	assert.Nil(t, err)
	assert.Equal(t, uint(5), cache.maxEvictionCount)
}
