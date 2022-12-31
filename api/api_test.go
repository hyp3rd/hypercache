package api

import (
	"testing"
	"time"

	"github.com/hyp3rd/hypercache"
)

func TestCache_SetCapacity(t *testing.T) {
	// Create a new cache with capacity 3
	cache, err := NewCache(3)
	if err != nil {
		t.Fatal(err)
	}

	// Add 3 items to the cache
	cache.Set("key1", 1, hypercache.WithDuration(5*time.Minute))
	time.Sleep(time.Second)
	cache.Set("key2", 2, hypercache.WithDuration(5*time.Minute))
	time.Sleep(time.Second)
	cache.Set("key3", 3, hypercache.WithDuration(5*time.Minute))

	// Check that the correct items are in the cache
	if v, ok := cache.Get("key1"); !ok || v.(int) != 1 {
		t.Errorf("expected value 1 for key key1, got %v", v)
	}
	time.Sleep(time.Second)
	if v, ok := cache.Get("key2"); !ok || v.(int) != 2 {
		t.Errorf("expected value 2 for key key2, got %v", v)
	}
	// time.Sleep(20 * time.Second)

	// Set the capacity to 2
	if err := cache.SetCapacity(2); err != nil {
		t.Error(err)
	}

	// Check that the cache size is 2
	if cache.Len() != 2 {
		t.Errorf("expected cache size 2, got %d", cache.Len())
	}

	// Check that the correct items are in the cache
	if v, ok := cache.Get("key1"); !ok || v.(int) != 1 {
		t.Errorf("expected value 1 for key key1, got %v", v)
	}
	if v, ok := cache.Get("key2"); !ok || v.(int) != 2 {
		t.Errorf("expected value 2 for key key2, got %v", v)
	}
	if _, ok := cache.Get("key3"); ok {
		t.Error("expected key key3 to be evicted")
	}

	// Set the capacity to 1
	if err := cache.SetCapacity(1); err != nil {
		t.Error(err)
	}

	// Check that the cache size is 1
	if cache.Len() != 1 {
		t.Errorf("expected cache size 1, got %d", cache.Len())
	}

	// Check that the correct item is in the cache
	if v, ok := cache.Get("key1"); !ok || v.(int) != 1 {
		t.Errorf("expected value 1 for key key1, got %v", v)
	}
	if _, ok := cache.Get("key2"); ok {
		t.Error("expected key key2 to be evicted")
	}
	if _, ok := cache.Get("key3"); ok {
		t.Error("expected key key3 to be evicted")
	}
}

func TestCache_Set(t *testing.T) {
	// Create a new cache with a capacity of 3
	cache, err := NewCache(3)
	if err != nil {
		t.Fatal(err)
	}

	// Set three items in the cache
	err = cache.Set("key1", "value1")
	if err != nil {
		t.Error(err)
	}
	err = cache.Set("key2", "value2")
	if err != nil {
		t.Error(err)
	}
	err = cache.Set("key3", "value3")
	if err != nil {
		t.Error(err)
	}

	// Check that the cache has a length of 3
	if cache.Len() != 3 {
		t.Errorf("Expected cache length to be 3, got %d", cache.Len())
	}
}
