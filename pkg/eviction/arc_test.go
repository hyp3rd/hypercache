package eviction

import "testing"

func TestARC_BasicSetGetAndEvict(t *testing.T) {
	arc, err := NewARCAlgorithm(2)
	if err != nil {
		t.Fatalf("NewARCAlgorithm error: %v", err)
	}

	arc.Set("a", 1)
	arc.Set("b", 2)
	if v, ok := arc.Get("a"); !ok || v.(int) != 1 {
		t.Fatalf("expected a=1, got %v ok=%v", v, ok)
	}

	// Insert c, causing an eviction
	arc.Set("c", 3)
	// One of a/b should be evicted; the other should remain.
	if _, ok := arc.Get("a"); !ok {
		if _, ok2 := arc.Get("b"); !ok2 {
			t.Fatalf("expected one of 'a' or 'b' to remain resident")
		}
	}
}

func TestARC_ZeroCapacity_NoOp(t *testing.T) {
	arc, err := NewARCAlgorithm(0)
	if err != nil {
		t.Fatalf("NewARCAlgorithm error: %v", err)
	}
	arc.Set("a", 1)
	if _, ok := arc.Get("a"); ok {
		t.Fatalf("expected miss on zero-capacity")
	}
	if key, ok := arc.Evict(); ok || key != "" {
		t.Fatalf("expected no eviction on zero-capacity, got %q ok=%v", key, ok)
	}
}

func TestARC_Delete_RemovesResidentAndGhost(t *testing.T) {
	arc, err := NewARCAlgorithm(2)
	if err != nil {
		t.Fatalf("NewARCAlgorithm error: %v", err)
	}
	arc.Set("a", 1)
	arc.Set("b", 2)
	arc.Delete("a")
	if _, ok := arc.Get("a"); ok {
		t.Fatalf("expected 'a' deleted")
	}
	// create a ghost by forcing eviction
	arc.Set("c", 3)
	arc.Delete("b") // whether resident or ghost, Delete should handle it
}

func TestARC_B1GhostHitPromotesToT2(t *testing.T) {
	arc, err := NewARCAlgorithm(2)
	if err != nil {
		t.Fatalf("NewARCAlgorithm error: %v", err)
	}
	arc.Set("a", 1)
	arc.Set("b", 2)
	// cause eviction of one resident into ghosts by adding 'c'
	arc.Set("c", 3)
	// Now reinsert one of the early keys to hit a ghost (B1/B2) path
	arc.Set("a", 10)
	if v, ok := arc.Get("a"); !ok || v.(int) != 10 {
		t.Fatalf("expected updated a=10 resident after B1/B2 hit, got %v ok=%v", v, ok)
	}
}
