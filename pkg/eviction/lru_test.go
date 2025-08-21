package eviction

import "testing"

func TestLRU_EvictsLeastRecentlyUsedOnSet(t *testing.T) {
	lru, err := NewLRUAlgorithm(2)
	if err != nil {
		t.Fatalf("NewLRUAlgorithm error: %v", err)
	}

	lru.Set("a", 1)
	lru.Set("b", 2)

	// Access "a" so that "b" becomes the least recently used
	if _, ok := lru.Get("a"); !ok {
		t.Fatalf("expected to get 'a'")
	}

	// Insert "c"; should evict "b"
	lru.Set("c", 3)
	if _, ok := lru.Get("b"); ok {
		t.Fatalf("expected 'b' to be evicted")
	}
	if _, ok := lru.Get("a"); !ok {
		t.Fatalf("expected 'a' to remain in cache")
	}
	if v, ok := lru.Get("c"); !ok || v.(int) != 3 {
		t.Fatalf("expected 'c'=3 in cache, got %v, ok=%v", v, ok)
	}
}

func TestLRU_EvictMethodOrder(t *testing.T) {
	lru, err := NewLRUAlgorithm(2)
	if err != nil {
		t.Fatalf("NewLRUAlgorithm error: %v", err)
	}

	lru.Set("a", 1)
	lru.Set("b", 2)

	// After two inserts, tail should be "a"
	key, ok := lru.Evict()
	if !ok || key != "a" {
		t.Fatalf("expected to evict 'a' first, got %q ok=%v", key, ok)
	}
	key, ok = lru.Evict()
	if !ok || key != "b" {
		t.Fatalf("expected to evict 'b' second, got %q ok=%v", key, ok)
	}
}
