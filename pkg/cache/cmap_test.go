package cache

import (
	"fmt"
	"sync"
	"testing"

	"github.com/goccy/go-json"
)

type testStringer struct {
	value string
}

func (t testStringer) String() string {
	return t.value
}

func TestNew(t *testing.T) {
	cmap := New[int]()
	if cmap.Count() != 0 {
		t.Errorf("Expected count 0, got %d", cmap.Count())
	}

	if !cmap.IsEmpty() {
		t.Error("Expected map to be empty")
	}
}

func TestNewStringer(t *testing.T) {
	cmap := NewStringer[testStringer, int]()
	if cmap.Count() != 0 {
		t.Errorf("Expected count 0, got %d", cmap.Count())
	}
}

func TestNewWithCustomShardingFunction(t *testing.T) {
	customSharding := func(key string) uint32 {
		return 0 // Always return 0 for testing
	}

	cmap := NewWithCustomShardingFunction[string, int](customSharding)
	if cmap.Count() != 0 {
		t.Errorf("Expected count 0, got %d", cmap.Count())
	}
}

func TestSetAndGet(t *testing.T) {
	cmap := New[int]()
	key := "test"
	value := 42

	cmap.Set(key, value)

	got, exists := cmap.Get(key)
	if !exists {
		t.Error("Expected key to exist")
	}

	if got != value {
		t.Errorf("Expected %d, got %d", value, got)
	}
}

func TestMSet(t *testing.T) {
	cmap := New[int]()
	data := map[string]int{
		"key1": 1,
		"key2": 2,
		"key3": 3,
	}

	cmap.MSet(data)

	for key, expected := range data {
		if got, exists := cmap.Get(key); !exists || got != expected {
			t.Errorf("Expected %d for key %s, got %d (exists: %v)", expected, key, got, exists)
		}
	}
}

func TestUpsert(t *testing.T) {
	cmap := New[int]()
	key := "test"

	// Insert new value
	result := cmap.Upsert(key, 10, func(exist bool, valueInMap, newValue int) int {
		if !exist {
			return newValue
		}

		return valueInMap + newValue
	})

	if result != 10 {
		t.Errorf("Expected 10, got %d", result)
	}

	// Update existing value
	result = cmap.Upsert(key, 5, func(exist bool, valueInMap, newValue int) int {
		if !exist {
			return newValue
		}

		return valueInMap + newValue
	})

	if result != 15 {
		t.Errorf("Expected 15, got %d", result)
	}
}

func TestSetIfAbsent(t *testing.T) {
	cmap := New[int]()
	key := "test"
	value := 42

	// Should set successfully
	if !cmap.SetIfAbsent(key, value) {
		t.Error("Expected SetIfAbsent to return true for new key")
	}

	// Should not set again
	if cmap.SetIfAbsent(key, 100) {
		t.Error("Expected SetIfAbsent to return false for existing key")
	}

	if got, _ := cmap.Get(key); got != value {
		t.Errorf("Expected %d, got %d", value, got)
	}
}

func TestHas(t *testing.T) {
	cmap := New[int]()
	key := "test"

	if cmap.Has(key) {
		t.Error("Expected key to not exist")
	}

	cmap.Set(key, 42)

	if !cmap.Has(key) {
		t.Error("Expected key to exist")
	}
}

func TestRemove(t *testing.T) {
	cmap := New[int]()
	key := "test"
	cmap.Set(key, 42)

	err := cmap.Remove(key)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if cmap.Has(key) {
		t.Error("Expected key to be removed")
	}
}

func TestRemoveCb(t *testing.T) {
	cmap := New[int]()
	key := "test"
	value := 42
	cmap.Set(key, value)

	removed := cmap.RemoveCb(key, func(k string, v int, exists bool) bool {
		if !exists {
			t.Error("Expected key to exist in callback")
		}

		if v != value {
			t.Errorf("Expected value %d in callback, got %d", value, v)
		}

		return true
	})

	if !removed {
		t.Error("Expected RemoveCb to return true")
	}

	if cmap.Has(key) {
		t.Error("Expected key to be removed")
	}
}

func TestPop(t *testing.T) {
	cmap := New[int]()
	key := "test"
	value := 42
	cmap.Set(key, value)

	got, exists := cmap.Pop(key)
	if !exists {
		t.Error("Expected key to exist")
	}

	if got != value {
		t.Errorf("Expected %d, got %d", value, got)
	}

	if cmap.Has(key) {
		t.Error("Expected key to be removed after pop")
	}
}

func TestCount(t *testing.T) {
	cmap := New[int]()

	if cmap.Count() != 0 {
		t.Errorf("Expected count 0, got %d", cmap.Count())
	}

	cmap.Set("key1", 1)
	cmap.Set("key2", 2)

	if cmap.Count() != 2 {
		t.Errorf("Expected count 2, got %d", cmap.Count())
	}
}

func TestIsEmpty(t *testing.T) {
	cmap := New[int]()

	if !cmap.IsEmpty() {
		t.Error("Expected map to be empty")
	}

	cmap.Set("key", 1)

	if cmap.IsEmpty() {
		t.Error("Expected map to not be empty")
	}
}

func TestKeys(t *testing.T) {
	cmap := New[int]()
	expectedKeys := []string{"key1", "key2", "key3"}

	for _, key := range expectedKeys {
		cmap.Set(key, 1)
	}

	keys := cmap.Keys()
	if len(keys) != len(expectedKeys) {
		t.Errorf("Expected %d keys, got %d", len(expectedKeys), len(keys))
	}

	keyMap := make(map[string]bool)
	for _, key := range keys {
		keyMap[key] = true
	}

	for _, expected := range expectedKeys {
		if !keyMap[expected] {
			t.Errorf("Expected key %s not found", expected)
		}
	}
}

func TestItems(t *testing.T) {
	cmap := New[int]()
	expected := map[string]int{
		"key1": 1,
		"key2": 2,
		"key3": 3,
	}

	for key, value := range expected {
		cmap.Set(key, value)
	}

	items := cmap.Items()
	if len(items) != len(expected) {
		t.Errorf("Expected %d items, got %d", len(expected), len(items))
	}

	for key, expectedValue := range expected {
		if got, exists := items[key]; !exists || got != expectedValue {
			t.Errorf("Expected %d for key %s, got %d (exists: %v)", expectedValue, key, got, exists)
		}
	}
}

func TestIterCb(t *testing.T) {
	cmap := New[int]()
	expected := map[string]int{
		"key1": 1,
		"key2": 2,
		"key3": 3,
	}

	for key, value := range expected {
		cmap.Set(key, value)
	}

	visited := make(map[string]int)
	cmap.IterCb(func(key string, value int) {
		visited[key] = value
	})

	if len(visited) != len(expected) {
		t.Errorf("Expected %d items visited, got %d", len(expected), len(visited))
	}

	for key, expectedValue := range expected {
		if got, exists := visited[key]; !exists || got != expectedValue {
			t.Errorf("Expected %d for key %s, got %d (exists: %v)", expectedValue, key, got, exists)
		}
	}
}

func TestIterBuffered(t *testing.T) {
	cmap := New[int]()
	expected := map[string]int{
		"key1": 1,
		"key2": 2,
		"key3": 3,
	}

	for key, value := range expected {
		cmap.Set(key, value)
	}

	visited := make(map[string]int)
	for item := range cmap.IterBuffered() {
		visited[item.Key] = item.Val
	}

	if len(visited) != len(expected) {
		t.Errorf("Expected %d items visited, got %d", len(expected), len(visited))
	}

	for key, expectedValue := range expected {
		if got, exists := visited[key]; !exists || got != expectedValue {
			t.Errorf("Expected %d for key %s, got %d (exists: %v)", expectedValue, key, got, exists)
		}
	}
}

func TestClear(t *testing.T) {
	cmap := New[int]()
	cmap.Set("key1", 1)
	cmap.Set("key2", 2)

	err := cmap.Clear()
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if !cmap.IsEmpty() {
		t.Error("Expected map to be empty after clear")
	}
}

func TestMarshalJSON(t *testing.T) {
	cmap := New[int]()
	expected := map[string]int{
		"key1": 1,
		"key2": 2,
	}

	for key, value := range expected {
		cmap.Set(key, value)
	}

	data, err := cmap.MarshalJSON()
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	var result map[string]int

	err = json.Unmarshal(data, &result)
	if err != nil {
		t.Errorf("Failed to unmarshal: %v", err)
	}

	for key, expectedValue := range expected {
		if got, exists := result[key]; !exists || got != expectedValue {
			t.Errorf("Expected %d for key %s, got %d (exists: %v)", expectedValue, key, got, exists)
		}
	}
}

func TestUnmarshalJSON(t *testing.T) {
	expected := map[string]int{
		"key1": 1,
		"key2": 2,
	}

	data, err := json.Marshal(expected)
	if err != nil {
		t.Errorf("Failed to marshal: %v", err)
	}

	cmap := New[int]()

	err = cmap.UnmarshalJSON(data)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	for key, expectedValue := range expected {
		if got, exists := cmap.Get(key); !exists || got != expectedValue {
			t.Errorf("Expected %d for key %s, got %d (exists: %v)", expectedValue, key, got, exists)
		}
	}
}

func TestGetShard(t *testing.T) {
	cmap := New[int]()

	shard := cmap.GetShard("test")
	if shard == nil {
		t.Error("Expected non-nil shard")
	}
}

func TestConcurrency(t *testing.T) {
	cmap := New[int]()

	var wg sync.WaitGroup

	numGoroutines := 100
	numOperations := 1000

	// Concurrent writes
	wg.Add(numGoroutines)

	for i := range numGoroutines {
		go func(id int) {
			defer wg.Done()

			for j := range numOperations {
				key := fmt.Sprintf("key-%d-%d", id, j)
				cmap.Set(key, id*numOperations+j)
			}
		}(i)
	}

	// Concurrent reads
	wg.Add(numGoroutines)

	for i := range numGoroutines {
		go func(id int) {
			defer wg.Done()

			for j := range numOperations {
				key := fmt.Sprintf("key-%d-%d", id, j)
				cmap.Get(key)
			}
		}(i)
	}

	wg.Wait()
}

func TestFnv32(t *testing.T) {
	hash1 := fnv32("test")
	hash2 := fnv32("test")
	hash3 := fnv32("different")

	if hash1 != hash2 {
		t.Error("Same strings should produce same hash")
	}

	if hash1 == hash3 {
		t.Error("Different strings should produce different hashes")
	}
}

func TestStrfnv32(t *testing.T) {
	str1 := testStringer{"test"}
	str2 := testStringer{"test"}
	str3 := testStringer{"different"}

	hash1 := strfnv32(str1)
	hash2 := strfnv32(str2)
	hash3 := strfnv32(str3)

	if hash1 != hash2 {
		t.Error("Same strings should produce same hash")
	}

	if hash1 == hash3 {
		t.Error("Different strings should produce different hashes")
	}
}
