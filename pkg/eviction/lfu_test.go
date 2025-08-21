package eviction

import "testing"

// Test that when two items have equal frequency, the older (least-recent) is evicted first.
func TestLFU_EvictsOldestOnTie_InsertOrder(t *testing.T) {
	lfu, err := NewLFUAlgorithm(2)
	if err != nil {
		t.Fatalf("NewLFUAlgorithm error: %v", err)
	}

	lfu.Set("a", 1)
	lfu.Set("b", 2)

	// Both have count=1, but "a" is older (inserted first), so it should be evicted first.
	key, ok := lfu.Evict()
	if !ok {
		t.Fatalf("expected an eviction, got none")
	}
	if key != "a" {
		t.Fatalf("expected 'a' to be evicted first on tie, got %q", key)
	}

	// Next eviction should evict the remaining one
	key, ok = lfu.Evict()
	if !ok {
		t.Fatalf("expected a second eviction, got none")
	}
	if key != "b" {
		t.Fatalf("expected 'b' to be evicted second, got %q", key)
	}
}

// Test that recency is used to break ties after accesses with equalized frequency.
func TestLFU_EvictsOldestOnTie_AccessOrder(t *testing.T) {
	lfu, err := NewLFUAlgorithm(2)
	if err != nil {
		t.Fatalf("NewLFUAlgorithm error: %v", err)
	}

	lfu.Set("a", 1)
	lfu.Set("b", 2)

	// Make both have the same frequency (2) but different recency orders.
	// Access sequence: bump "b" first, then "a" -> both count=2, and "a" is more recent.
	if _, ok := lfu.Get("b"); !ok {
		t.Fatalf("expected to get 'b'")
	}
	if _, ok := lfu.Get("a"); !ok {
		t.Fatalf("expected to get 'a'")
	}

	// On tie (count=2), the older (least-recent) is "b" now, so it should be evicted first.
	key, ok := lfu.Evict()
	if !ok {
		t.Fatalf("expected an eviction, got none")
	}
	if key != "b" {
		t.Fatalf("expected 'b' to be evicted first on tie after accesses, got %q", key)
	}
}
