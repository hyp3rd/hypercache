package hypercache

import (
	"testing"
)

func TestHyperCache_GetSet(t *testing.T) {
	cache, err := NewHyperCache(3)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	defer cache.Close()

	// Test getting a non-existent key
	value, ok := cache.Get("key1")
	if ok {
		t.Error("expected ok to be false, got true")
	}
	if value != nil {
		t.Error("expected value to be nil, got", value)
	}

	// Test setting and getting a key
	value2 := "value2"
	cache.Set("key2", value2, 0)
	value3, ok := cache.Get("key2")
	if !ok {
		t.Error("expected ok to be true, got false")
	}
	if value3 != value2 {
		t.Error("expected value to be value2, got", value3)
	}

	// Test getting a key with a capacity of 0
	cache, err = NewHyperCache(0)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	defer cache.Close()
	cache.Set("key3", "value3", 0)
	value4, ok := cache.Get("key3")
	if !ok {
		t.Error("expected ok to be true, got false")
	}
	if value4 != "value3" {
		t.Error("expected value to be 'value3', got", value4)
	}
}

func TestHyperCache_GetOrSet(t *testing.T) {
	cache, err := NewHyperCache(2)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	defer cache.Close()

	// Test getting a non-existent key with a default value
	value, err := cache.GetOrSet("key1", "default", 0)
	if err != nil {
		t.Error("unexpected error:", err)
	}
	if value != "default" {
		t.Error("expected value to be 'default', got", value)
	}
	value2, ok := cache.Get("key1")
	if !ok {
		t.Error("expected ok to be true, got false")
	}
	if value2 != "default" {
		t.Error("expected value to be 'default', got", value2)
	}

	// Test getting a key with a value
	cache.Set("key2", "value2", 0)
	value3, err := cache.GetOrSet("key2", "default", 0)
	if err != nil {
		t.Error("unexpected error:", err)
	}
	if value3 != "value2" {
		t.Error("expected value to be 'value2', got", value3)
	}
}

func TestHyperCache_GetMultiple(t *testing.T) {
	cache, err := NewHyperCache(3)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	defer cache.Close()

	// Test getting multiple non-existent keys
	values := cache.GetMultiple("key1", "key2")

	if len(values) != 0 {
		t.Error("expected len(values) to be 0, got", len(values))
	}
	if values["key1"] != nil || values["key2"] != nil {
		t.Error("expected values to be nil, got", values)
	}

	// Test getting multiple keys with values
	cache.Set("key1", "value1", 0)
	cache.Set("key2", "value2", 0)
	values2 := cache.GetMultiple("key1", "key2")

	if len(values2) != 2 {
		t.Error("expected len(values2) to be 2, got", len(values2))
	}
	if values2["key1"] != "value1" || values2["key2"] != "value2" {
		t.Error("expected values to be ['value1', 'value2'], got", values2)
	}

	// Test getting multiple keys with some values and some non-existent keys
	values3 := cache.GetMultiple("key1", "key2", "key3")

	if len(values3) != 2 {
		t.Error("expected len(values3) to be 3, got", len(values3))
	}
	if values3["key1"] != "value1" || values3["key2"] != "value2" || values3["key3"] != nil {
		t.Error("expected values to be ['value1', 'value2', nil], got", values3)
	}
}
