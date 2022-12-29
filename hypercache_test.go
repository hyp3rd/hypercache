package hypercache

import (
	"fmt"
	"testing"
	"time"
)

func TestNewHyperCache(t *testing.T) {
	// Test with valid capacity
	cache, err := NewHyperCache(10)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if cache.capacity != 10 {
		t.Errorf("unexpected capacity: %d", cache.capacity)
	}
	if cache.lru == nil {
		t.Error("lru list is nil")
	}
	if cache.itemsByKey == nil {
		t.Error("itemsByKey map is nil")
	}
	if cache.stop == nil {
		t.Error("stop channel is nil")
	}
	if cache.evictCh == nil {
		t.Error("evictCh channel is nil")
	}

	// Test with invalid capacity
	_, err = NewHyperCache(-1)
	if err == nil {
		t.Error("expected error for invalid capacity, got nil")
	}
}

func TestHyperCache_Set(t *testing.T) {
	cache, _ := NewHyperCache(10)

	// Test with empty key
	err := cache.Set("", "value", time.Minute)
	if err == nil {
		t.Error("expected error for empty key, got nil")
	}

	// Test with nil value
	err = cache.Set("key", nil, time.Minute)
	if err == nil {
		t.Error("expected error for nil value, got nil")
	}

	// Test with negative duration
	err = cache.Set("key", "value", -time.Minute)
	if err == nil {
		t.Error("expected error for negative duration, got nil")
	}

	// Test with valid key, value, and duration
	err = cache.Set("key", "value", time.Minute)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(cache.itemsByKey) != 1 {
		t.Errorf("unexpected number of items in cache: %d", len(cache.itemsByKey))
	}
	if ee, ok := cache.itemsByKey["key"]; !ok {
		t.Error("key not found in itemsByKey map")
	} else {
		item := ee.Value.(*CacheItem)
		if item.Key != "key" {
			t.Errorf("unexpected key: %s", item.Key)
		}
		if item.Value != "value" {
			t.Errorf("unexpected value: %v", item.Value)
		}
		if item.Duration != time.Minute {
			t.Errorf("unexpected duration: %s", item.Duration)
		}
		if item.LastAccessedBefore.IsZero() {
			t.Error("last accessed time is zero")
		}
	}

	// Test with valid key, value, and duration when the cache is full
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("key%d", i)
		// t.Log(key)
		err = cache.Set(key, "value", time.Minute)

		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	}

	items := cache.List()
	for _, item := range items {
		t.Log(item.Key)
	}

	err = cache.Set("key10", "value", time.Minute)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if cache.Len() != 10 {
		t.Errorf("unexpected number of items in cache: %d", cache.Len())
	}
	if _, ok := cache.itemsByKey["key0"]; ok {
		t.Error("key0 found in itemsByKey map, expected it to be evicted")
	}
	if _, ok := cache.itemsByKey["key10"]; !ok {
		t.Error("key10 not found in itemsByKey map")
	}

	// Test with valid key, value, and duration when the key already exists in the cache
	err = cache.Set("key10", "new value", 2*time.Minute)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(cache.itemsByKey) != 10 {
		t.Errorf("unexpected number of items in cache: %d", len(cache.itemsByKey))
	}
	if ee, ok := cache.itemsByKey["key10"]; !ok {
		t.Error("key10 not found in itemsByKey map")
	} else {
		item := ee.Value.(*CacheItem)
		if item.Key != "key10" {
			t.Errorf("unexpected key: %s", item.Key)
		}
		if item.Value != "new value" {
			t.Errorf("unexpected value: %v", item.Value)
		}
		if item.Duration != 2*time.Minute {
			t.Errorf("unexpected duration: %s", item.Duration)
		}
		if item.LastAccessedBefore.IsZero() {
			t.Error("last accessed time is zero")
		}
	}
}

func TestHyperCache_Get(t *testing.T) {
	cache, _ := NewHyperCache(10)

	// Test with non-existent key
	_, ok := cache.Get("key")
	if ok {
		t.Error("expected error for non-existent key, got nil")
	}

	// Test with expired key
	cache.Set("key", "value", -time.Minute)
	_, ok = cache.Get("key")
	if ok {
		t.Error("expected error for expired key, got nil")
	}

	// Test with valid key
	cache.Set("key", "value", time.Minute)
	value, ok := cache.Get("key")

	if !ok {
		t.Errorf("unexpected error while getting the key")
	}
	if value != "value" {
		t.Errorf("unexpected value: %v", value)
	}
	if ee, ok := cache.itemsByKey["key"]; !ok {
		t.Error("key not found in itemsByKey map")
	} else {
		item := ee.Value.(*CacheItem)
		if item.Key != "key" {
			t.Errorf("unexpected key: %s", item.Key)
		}
		if item.Value != "value" {
			t.Errorf("unexpected value: %v", item.Value)
		}
		if item.Duration != time.Minute {
			t.Errorf("unexpected duration: %s", item.Duration)
		}
		if item.LastAccessedBefore.IsZero() {
			t.Error("last accessed time is zero")
		}
	}
}

func TestHyperCache_Delete(t *testing.T) {
	cache, _ := NewHyperCache(10)

	// Test with non-existent key
	err := cache.Delete("key")
	if err == nil {
		t.Error("expected error for non-existent key, got nil")
	}

	// Test with valid key
	cache.Set("key", "value", time.Minute)
	err = cache.Delete("key")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(cache.itemsByKey) != 0 {
		t.Errorf("unexpected number of items in cache: %d", len(cache.itemsByKey))
	}
	if _, ok := cache.itemsByKey["key"]; ok {
		t.Error("key found in itemsByKey map, expected it to be deleted")
	}
}

func TestHyperCache_Clear(t *testing.T) {
	cache, _ := NewHyperCache(10)

	// Test with empty cache
	cache.Clear()
	if len(cache.itemsByKey) != 0 {
		t.Errorf("unexpected number of items in cache: %d", len(cache.itemsByKey))
	}

	// Test with non-empty cache
	cache.Set("key", "value", time.Minute)
	cache.Clear()
	if len(cache.itemsByKey) != 0 {
		t.Errorf("unexpected number of items in cache: %d", len(cache.itemsByKey))
	}
	if _, ok := cache.itemsByKey["key"]; ok {
		t.Error("key found in itemsByKey map, expected it to be deleted")
	}
}

func TestHyperCache_Capacity(t *testing.T) {
	cache, _ := NewHyperCache(10)

	// Test with valid capacity
	cache.SetCapacity(5)
	if cache.capacity != 5 {
		t.Errorf("unexpected capacity: %d", cache.capacity)
	}

	// Test with invalid capacity
	err := cache.SetCapacity(-1)
	if err == nil {
		t.Error("expected error for invalid capacity, got nil")
	}
	if cache.capacity != 5 {
		t.Errorf("unexpected capacity: %d", cache.capacity)
	}
}

func TestHyperCache_Len(t *testing.T) {
	cache, _ := NewHyperCache(10)

	// Test with empty cache
	if cache.Len() != 0 {
		t.Errorf("unexpected number of items in cache: %d", cache.Len())
	}

	// Test with non-empty cache
	cache.Set("key", "value", time.Minute)
	if cache.Len() != 1 {
		t.Errorf("unexpected number of items in cache: %d", cache.Len())
	}
}

func TestHyperCache_expirationLoop(t *testing.T) {
	cache, _ := NewHyperCache(10)

	// Test with empty cache
	cache.expirationLoop()
	if len(cache.itemsByKey) != 0 {
		t.Errorf("unexpected number of items in cache: %d", len(cache.itemsByKey))
	}

	// Test with non-empty cache
	cache.Set("key", "value", time.Minute)
	cache.expirationLoop()
	if len(cache.itemsByKey) != 1 {
		t.Errorf("unexpected number of items in cache: %d", len(cache.itemsByKey))
	}

	// Test with expired key
	cache.Set("key", "value", 1*time.Second)
	time.Sleep(1 * time.Second)
	cache.expirationLoop()
	if len(cache.itemsByKey) != 0 {
		t.Errorf("unexpected number of items in cache: %d", len(cache.itemsByKey))
	}
}

// func TestHyperCache_evictionLoop(t *testing.T) {
// 	cache, _ := NewHyperCache(10)

// 	// Test with empty cache
// 	cache.evictionLoop()
// 	if len(cache.itemsByKey) != 0 {
// 		t.Errorf("unexpected number of items in cache: %d", len(cache.itemsByKey))
// 	}

// 	// Test with non-empty cache
// 	cache.Set("key", "value", time.Minute)
// 	cache.evictionLoop()
// 	if len(cache.itemsByKey) != 1 {
// 		t.Errorf("unexpected number of items in cache: %d", len(cache.itemsByKey))
// 	}

// 	// Test with full cache
// 	for i := 0; i < 10; i++ {
// 		key := fmt.Sprintf("key%d", i)
// 		err := cache.Set(key, "value", time.Minute)
// 		if err != nil {
// 			t.Errorf("unexpected error: %v", err)
// 		}
// 	}
// 	cache.evictionLoop()
// 	if len(cache.itemsByKey) != 10 {
// 		t.Errorf("unexpected number of items in cache: %d", len(cache.itemsByKey))
// 	}
// 	if _, ok := cache.itemsByKey["key0"]; !ok {
// 		t.Error("key0 not found in itemsByKey map, expected it to be present")
// 	}
// 	if _, ok := cache.itemsByKey["key9"]; !ok {
// 		t.Error("key9 not found in itemsByKey map, expected it to be present")
// 	}
// }

func TestHyperCache_Close(t *testing.T) {
	cache, _ := NewHyperCache(10)

	// Test with empty cache
	cache.Close()
	if len(cache.itemsByKey) != 0 {
		t.Errorf("unexpected number of items in cache: %d", len(cache.itemsByKey))
	}

	cache, _ = NewHyperCache(10)

	// Test with non-empty cache
	cache.Set("key", "value", time.Minute)
	cache.Close()
	if len(cache.itemsByKey) != 0 {
		t.Errorf("unexpected number of items in cache: %d", len(cache.itemsByKey))
	}
	if _, ok := cache.itemsByKey["key"]; ok {
		t.Error("key found in itemsByKey map, expected it to be deleted")
	}
}
