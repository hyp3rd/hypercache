package eviction

import "testing"

func TestClock_EvictsWhenHandFindsColdPage(t *testing.T) {
	clk, err := NewClockAlgorithm(2)
	if err != nil {
		t.Fatalf("NewClockAlgorithm error: %v", err)
	}

	clk.Set("a", 1)
	clk.Set("b", 2)

	// Touch 'a' once to increment its AccessCount; leave 'b' cold
	if _, ok := clk.Get("a"); !ok {
		t.Fatalf("expected to get 'a'")
	}

	// Eviction may require multiple passes due to access count decrements.
	// Loop until 'b' is evicted (within a few attempts).
	var (
		key string
		ok  bool
	)
	for range 3 {
		key, ok = clk.Evict()
		if ok && key == "b" {
			break
		}
	}

	if !ok || key != "b" {
		t.Fatalf("expected to evict 'b' first within retries, got %q ok=%v", key, ok)
	}

	// Now evict the remaining 'a', possibly requiring one more pass.
	key, ok = clk.Evict()
	if !ok {
		key, ok = clk.Evict()
	}

	if !ok || key != "a" {
		t.Fatalf("expected to evict 'a' second, got %q ok=%v", key, ok)
	}
}

func TestClock_ZeroCapacity_NoOp(t *testing.T) {
	clk, err := NewClockAlgorithm(0)
	if err != nil {
		t.Fatalf("NewClockAlgorithm error: %v", err)
	}

	clk.Set("a", 1)

	if _, ok := clk.Get("a"); ok {
		t.Fatalf("expected Get to miss on zero-capacity cache")
	}

	if key, ok := clk.Evict(); ok || key != "" {
		t.Fatalf("expected no eviction on zero-capacity, got %q ok=%v", key, ok)
	}
}

func TestClock_Delete_RemovesItem(t *testing.T) {
	clk, err := NewClockAlgorithm(2)
	if err != nil {
		t.Fatalf("NewClockAlgorithm error: %v", err)
	}

	clk.Set("a", 1)
	clk.Set("b", 2)
	clk.Delete("a")

	if _, ok := clk.Get("a"); ok {
		t.Fatalf("expected 'a' to be deleted")
	}

	// Evict should not return deleted key
	// Loop a couple of times due to potential decrements
	for range 3 {
		if key, ok := clk.Evict(); ok {
			if key == "a" {
				t.Fatalf("did not expect to evict deleted key 'a'")
			}

			if key == "b" {
				return
			}
		}
	}

	t.Fatalf("expected to eventually evict 'b'")
}
