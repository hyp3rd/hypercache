package eviction

import "testing"

// algoFactory constructs an algorithm with the given capacity.
type algoFactory func(capacity int) (IAlgorithm, error)

// runEvictionContract validates the IAlgorithm contract that every
// algorithm must satisfy regardless of its eviction policy:
//
//   - Set/Get/Delete behave as a key-value store within capacity.
//   - When at capacity, Set evicts a victim of the algorithm's choosing.
//   - Evict() returns false when empty.
//   - Capacity 0 is a no-op (Set/Get/Evict all return zero values).
//
// Algorithm-specific policy assertions (LRU recency order, LFU frequency
// order, ARC ghost behavior, etc.) belong in each algorithm's own _test
// file alongside this contract test.
func runEvictionContract(t *testing.T, name string, newAlgo algoFactory) {
	t.Helper()

	t.Run(name+"/EvictsOnSetWhenFull", func(t *testing.T) {
		t.Parallel()
		assertEvictsOnSetWhenFull(t, newAlgo)
	})

	t.Run(name+"/EvictReturnsFalseWhenEmpty", func(t *testing.T) {
		t.Parallel()
		assertEvictReturnsFalseWhenEmpty(t, newAlgo)
	})

	t.Run(name+"/ZeroCapacityNoOp", func(t *testing.T) {
		t.Parallel()
		assertZeroCapacityNoOp(t, newAlgo)
	})

	t.Run(name+"/DeleteRemovesItem", func(t *testing.T) {
		t.Parallel()
		assertDeleteRemovesItem(t, newAlgo)
	})
}

func assertEvictsOnSetWhenFull(t *testing.T, newAlgo algoFactory) {
	t.Helper()

	algo, err := newAlgo(2)
	if err != nil {
		t.Fatalf("newAlgo: %v", err)
	}

	algo.Set("a", 1)
	algo.Set("b", 2)

	// One Get on "a" — under any reasonable recency/frequency policy,
	// this makes "b" the eviction victim when "c" pushes capacity.
	if _, ok := algo.Get("a"); !ok {
		t.Fatalf("expected to find 'a' before eviction")
	}

	algo.Set("c", 3)

	if _, ok := algo.Get("b"); ok {
		t.Fatalf("expected 'b' to be evicted")
	}

	if _, ok := algo.Get("a"); !ok {
		t.Fatalf("expected 'a' to remain")
	}

	v, ok := algo.Get("c")
	if !ok {
		t.Fatalf("expected 'c' present after Set")
	}

	got, ok := v.(int)
	if !ok || got != 3 {
		t.Fatalf("expected 'c'=3, got %v", v)
	}
}

func assertEvictReturnsFalseWhenEmpty(t *testing.T, newAlgo algoFactory) {
	t.Helper()

	algo, err := newAlgo(4)
	if err != nil {
		t.Fatalf("newAlgo: %v", err)
	}

	key, ok := algo.Evict()
	if ok || key != "" {
		t.Fatalf("expected Evict() on empty algo to return (\"\", false), got (%q, %v)", key, ok)
	}
}

func assertZeroCapacityNoOp(t *testing.T, newAlgo algoFactory) {
	t.Helper()

	algo, err := newAlgo(0)
	if err != nil {
		t.Fatalf("newAlgo: %v", err)
	}

	algo.Set("a", 1)

	if _, ok := algo.Get("a"); ok {
		t.Fatalf("expected Get to miss on zero-capacity algo")
	}

	key, ok := algo.Evict()
	if ok || key != "" {
		t.Fatalf("expected no eviction on zero-capacity, got (%q, %v)", key, ok)
	}
}

func assertDeleteRemovesItem(t *testing.T, newAlgo algoFactory) {
	t.Helper()

	algo, err := newAlgo(2)
	if err != nil {
		t.Fatalf("newAlgo: %v", err)
	}

	algo.Set("a", 1)
	algo.Set("b", 2)
	algo.Delete("a")

	if _, ok := algo.Get("a"); ok {
		t.Fatalf("expected 'a' deleted")
	}

	// Remaining item should be "b" (only candidate).
	key, ok := algo.Evict()
	if !ok || key != "b" {
		t.Fatalf("expected to evict 'b' as remaining item, got (%q, %v)", key, ok)
	}
}

// The contract is run against algorithms whose recency/frequency-based policy
// makes "Get(a) then Set(c) evicts b" deterministic — LRU and CAWOLFU. Clock
// (clock-hand scan) and ARC (T1/T2/B1/B2 lists) have non-equivalent eviction
// semantics; they keep their own dedicated tests below in clock_test.go,
// arc_test.go, and lfu_test.go.

// TestLRUContract runs the IAlgorithm contract against the LRU implementation.
func TestLRUContract(t *testing.T) {
	t.Parallel()
	runEvictionContract(t, "lru", func(c int) (IAlgorithm, error) { return NewLRUAlgorithm(c) })
}

// TestCAWOLFUContract runs the IAlgorithm contract against the CAWOLFU implementation.
func TestCAWOLFUContract(t *testing.T) {
	t.Parallel()
	runEvictionContract(t, "cawolfu", func(c int) (IAlgorithm, error) { return NewCAWOLFU(c) })
}
