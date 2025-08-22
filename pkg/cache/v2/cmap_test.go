package cachev2

import (
	"sync"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	cm := New()
	if len(cm.shards) != ShardCount {
		t.Errorf("Expected %d shards, got %d", ShardCount, len(cm.shards))
	}

	for i, shard := range cm.shards {
		if shard == nil {
			t.Errorf("Shard %d is nil", i)

			return
		}

		if shard.items == nil {
			t.Errorf("Shard %d items map is nil", i)

			return
		}
		// no hasher field in v2 shards
	}
}

func TestGetShardIndex(t *testing.T) {
	tests := []struct {
		key string
	}{
		{"test"},
		{""},
		{"a"},
		{"very_long_key_to_test_hash_distribution"},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			index := getShardIndex(tt.key)
			if index >= ShardCount32 {
				t.Errorf("Shard index %d exceeds shard count %d", index, ShardCount32)
			}
		})
	}
}

func TestGetShard(t *testing.T) {
	cm := New()

	shard := cm.GetShard("test")
	if shard == nil {
		t.Error("GetShard returned nil")
	}

	// Test that same key returns same shard
	shard2 := cm.GetShard("test")
	if shard != shard2 {
		t.Error("Same key returned different shards")
	}
}

func TestSetAndGet(t *testing.T) {
	cm := New()
	item := &Item{
		Value:      "test_value",
		Expiration: time.Hour,
	}

	// Test Set and Get
	cm.Set("test_key", item)

	retrieved, ok := cm.Get("test_key")

	if !ok {
		t.Error("Expected to find item, but it was not found")
	}

	if retrieved != item {
		t.Error("Retrieved item does not match set item")
	}

	// Test Get non-existent key
	_, ok = cm.Get("non_existent")
	if ok {
		t.Error("Expected not to find item, but it was found")
	}
}

func TestHas(t *testing.T) {
	cm := New()
	item := &Item{
		Value:      "test_value",
		Expiration: time.Hour,
	}

	// Test Has with non-existent key
	if cm.Has("test_key") {
		t.Error("Has returned true for non-existent key")
	}

	// Test Has with existing key
	cm.Set("test_key", item)

	if !cm.Has("test_key") {
		t.Error("Has returned false for existing key")
	}
}

func TestPop(t *testing.T) {
	cm := New()
	item := &Item{
		Value:      "test_value",
		Expiration: time.Hour,
	}

	// Test Pop non-existent key
	popped, ok := cm.Pop("test_key")
	if ok || popped != nil {
		t.Error("Pop returned item for non-existent key")
	}

	// Test Pop existing key
	cm.Set("test_key", item)

	popped, ok = cm.Pop("test_key")
	if !ok || popped != item {
		t.Error("Pop did not return correct item")
	}

	// Verify item is removed
	if cm.Has("test_key") {
		t.Error("Item still exists after Pop")
	}
}

func TestRemove(t *testing.T) {
	cm := New()
	item := &Item{
		Value:      "test_value",
		Expiration: time.Hour,
	}

	cm.Set("test_key", item)
	cm.Remove("test_key")

	if cm.Has("test_key") {
		t.Error("Item still exists after Remove")
	}

	// Test Remove non-existent key (should not panic)
	cm.Remove("non_existent")
}

func TestCount(t *testing.T) {
	cm := New()

	if cm.Count() != 0 {
		t.Error("New map should have count 0")
	}

	item := &Item{
		Value:      "test_value",
		Expiration: time.Hour,
	}

	cm.Set("key1", item)

	if cm.Count() != 1 {
		t.Error("Count should be 1 after adding one item")
	}

	cm.Set("key2", item)

	if cm.Count() != 2 {
		t.Error("Count should be 2 after adding two items")
	}

	cm.Remove("key1")

	if cm.Count() != 1 {
		t.Error("Count should be 1 after removing one item")
	}
}

func TestIterBuffered(t *testing.T) {
	cm := New()
	items := map[string]*Item{
		"key1": {Value: "value1", Expiration: time.Hour},
		"key2": {Value: "value2", Expiration: time.Hour},
		"key3": {Value: "value3", Expiration: time.Hour},
	}

	// Set items
	for k, v := range items {
		cm.Set(k, v)
	}

	// Iterate and verify
	found := make(map[string]Item)
	for tuple := range cm.IterBuffered() {
		found[tuple.Key] = tuple.Val
	}

	if len(found) != len(items) {
		t.Errorf("Expected %d items from iterator, got %d", len(items), len(found))
	}

	for key, expectedItem := range items {
		if foundItem, ok := found[key]; !ok {
			t.Errorf("Key %s not found in iteration", key)
		} else if foundItem.Value != expectedItem.Value {
			t.Errorf("Value mismatch for key %s", key)
		}
	}
}

func TestClear(t *testing.T) {
	cm := New()
	items := map[string]*Item{
		"key1": {Value: "value1", Expiration: time.Hour},
		"key2": {Value: "value2", Expiration: time.Hour},
		"key3": {Value: "value3", Expiration: time.Hour},
	}

	// Set items
	for k, v := range items {
		cm.Set(k, v)
	}

	if cm.Count() != 3 {
		t.Error("Expected 3 items before clear")
	}

	cm.Clear()

	if cm.Count() != 0 {
		t.Error("Expected 0 items after clear")
	}
}

func TestConcurrentAccess(t *testing.T) {
	cm := New()
	wg := sync.WaitGroup{}

	// Concurrent writes
	for i := range 100 {
		wg.Add(1)

		go func(i int) {
			defer wg.Done()

			item := &Item{
				Value:      i,
				Expiration: time.Hour,
			}
			cm.Set(string(rune(i)), item)
		}(i)
	}

	// Concurrent reads
	for i := range 50 {
		wg.Add(1)

		go func(i int) {
			defer wg.Done()

			cm.Get(string(rune(i)))
		}(i)
	}

	wg.Wait()
}

func TestSnapshotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic for uninitialized ConcurrentMap")
		}
	}()

	var cm ConcurrentMap
	snapshot(&cm)
}

func BenchmarkSet(b *testing.B) {
	cm := New()
	item := &Item{
		Value:      "benchmark_value",
		Expiration: time.Hour,
	}

	b.ResetTimer()

	for i := range b.N {
		cm.Set(string(rune(i)), item)
	}
}

func BenchmarkGet(b *testing.B) {
	cm := New()
	item := &Item{
		Value:      "benchmark_value",
		Expiration: time.Hour,
	}

	// Pre-populate
	for i := range 1000 {
		cm.Set(string(rune(i)), item)
	}

	b.ResetTimer()

	for i := range b.N {
		cm.Get(string(rune(i % 1000)))
	}
}

func BenchmarkConcurrentSetGet(b *testing.B) {
	cm := New()
	item := &Item{
		Value:      "benchmark_value",
		Expiration: time.Hour,
	}

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if i%2 == 0 {
				cm.Set(string(rune(i)), item)
			} else {
				cm.Get(string(rune(i)))
			}

			i++
		}
	})
}
