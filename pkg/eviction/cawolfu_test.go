package eviction

import "testing"

func TestCAWOLFU_EvictsLeastFrequentTail(t *testing.T) {
	c, err := NewCAWOLFU(2)
	if err != nil {
		t.Fatalf("NewCAWOLFU error: %v", err)
	}

	c.Set("a", 1)
	c.Set("b", 2)
	// bump 'a' so 'b' is less frequent
	if _, ok := c.Get("a"); !ok {
		t.Fatalf("expected to get 'a'")
	}

	// Insert 'c' -> evict tail ('b')
	c.Set("c", 3)

	if _, ok := c.Get("b"); ok {
		t.Fatalf("expected 'b' to be evicted")
	}

	if _, ok := c.Get("a"); !ok {
		t.Fatalf("expected 'a' to remain in cache")
	}

	if v, ok := c.Get("c"); !ok || v.(int) != 3 {
		t.Fatalf("expected 'c'=3 in cache, got %v, ok=%v", v, ok)
	}
}

func TestCAWOLFU_EvictMethodOrder(t *testing.T) {
	c, err := NewCAWOLFU(2)
	if err != nil {
		t.Fatalf("NewCAWOLFU error: %v", err)
	}

	c.Set("a", 1)
	c.Set("b", 2)
	// Without additional access, tail is 'a' (inserted first with same count)
	key, ok := c.Evict()
	if !ok || key != "a" {
		t.Fatalf("expected to evict 'a' first, got %q ok=%v", key, ok)
	}

	key, ok = c.Evict()
	if !ok || key != "b" {
		t.Fatalf("expected to evict 'b' second, got %q ok=%v", key, ok)
	}
}

func TestCAWOLFU_ZeroCapacity_NoOp(t *testing.T) {
	c, err := NewCAWOLFU(0)
	if err != nil {
		t.Fatalf("NewCAWOLFU error: %v", err)
	}

	c.Set("a", 1)

	if _, ok := c.Get("a"); ok {
		t.Fatalf("expected Get to miss on zero-capacity cache")
	}

	if key, ok := c.Evict(); ok || key != "" {
		t.Fatalf("expected no eviction on zero-capacity, got %q ok=%v", key, ok)
	}
}

func TestCAWOLFU_Delete_RemovesItem(t *testing.T) {
	c, err := NewCAWOLFU(2)
	if err != nil {
		t.Fatalf("NewCAWOLFU error: %v", err)
	}

	c.Set("a", 1)
	c.Set("b", 2)
	c.Delete("a")

	if _, ok := c.Get("a"); ok {
		t.Fatalf("expected 'a' to be deleted")
	}

	key, ok := c.Evict()
	if !ok || key != "b" {
		t.Fatalf("expected to evict 'b' as remaining item, got %q ok=%v", key, ok)
	}
}
