package eviction

import (
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/hyp3rd/hypercache/internal/sentinel"
	cachev2 "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

func TestSharded_ConstructorRejectsBadShardCount(t *testing.T) {
	t.Parallel()

	cases := []int{0, -1, 3, 5, 7, 17}
	for _, n := range cases {
		_, err := NewSharded("lru", 100, n)
		if err == nil {
			t.Errorf("shardCount=%d: expected error, got nil", n)
		}
	}

	// Valid power-of-two sizes succeed.
	for _, n := range []int{1, 2, 4, 8, 16, 32, 64} {
		s, err := NewSharded("lru", 100, n)
		if err != nil {
			t.Errorf("shardCount=%d: unexpected error: %v", n, err)
		}

		if s == nil {
			t.Errorf("shardCount=%d: nil Sharded", n)
		}
	}
}

func TestSharded_RejectsNegativeCapacity(t *testing.T) {
	t.Parallel()

	_, err := NewSharded("lru", -1, 4)
	if !errors.Is(err, sentinel.ErrInvalidCapacity) {
		t.Errorf("expected ErrInvalidCapacity, got %v", err)
	}
}

func TestSharded_RejectsUnknownAlgorithm(t *testing.T) {
	t.Parallel()

	_, err := NewSharded("nonexistent_algo", 100, 4)
	if err == nil {
		t.Errorf("expected error for unknown algorithm name")
	}
}

// TestSharded_RoutesByConcurrentMapHash verifies the cache-locality invariant:
// keys hash to the same shard via cachev2.Hash, so a key's data shard and
// eviction shard align under matching shardCount.
func TestSharded_RoutesByConcurrentMapHash(t *testing.T) {
	t.Parallel()

	const shardCount = 32

	s, err := NewSharded("lru", 320, shardCount)
	if err != nil {
		t.Fatalf("NewSharded: %v", err)
	}

	// For each test key, verify the routed shard index matches what
	// cachev2.Hash would route to.
	for i := range 1000 {
		key := "key-" + strconv.Itoa(i)
		expected := cachev2.Hash(key) & (uint32(shardCount) - 1)

		// Inspect via the hash directly — no public accessor on Sharded
		// for shard index, so we recompute and trust mask consistency.
		if s.mask != uint32(shardCount-1) {
			t.Fatalf("Sharded.mask = %d, want %d", s.mask, shardCount-1)
		}

		got := cachev2.Hash(key) & s.mask
		if got != expected {
			t.Errorf("key=%q: routed to shard %d, ConcurrentMap would route to %d", key, got, expected)
		}
	}
}

// TestSharded_BasicSetGetDelete is the IAlgorithm contract.
func TestSharded_BasicSetGetDelete(t *testing.T) {
	t.Parallel()

	s, err := NewSharded("lru", 64, 8)
	if err != nil {
		t.Fatalf("NewSharded: %v", err)
	}

	// Set across many keys; keys should distribute across shards.
	for i := range 200 {
		s.Set("k"+strconv.Itoa(i), i)
	}

	// Get a recently-set key should succeed.
	if _, ok := s.Get("k199"); !ok {
		t.Errorf("Get(k199) missing")
	}

	// Delete should remove.
	s.Delete("k199")

	if _, ok := s.Get("k199"); ok {
		t.Errorf("Delete(k199) ineffective")
	}
}

// TestSharded_EvictDistributesAcrossShards verifies the round-robin Evict
// touches multiple shards rather than draining a single one.
func TestSharded_EvictDistributesAcrossShards(t *testing.T) {
	t.Parallel()

	const shardCount = 4

	s, err := NewSharded("lru", 32, shardCount)
	if err != nil {
		t.Fatalf("NewSharded: %v", err)
	}

	// Insert one key per shard (use deterministic keys we can verify routing for).
	keysPerShard := make(map[uint32][]string)

	for i := range 64 {
		key := "k" + strconv.Itoa(i)
		shardIdx := cachev2.Hash(key) & uint32(shardCount-1)

		keysPerShard[shardIdx] = append(keysPerShard[shardIdx], key)

		s.Set(key, i)
	}

	// Sanity: all 4 shards received at least one key (deterministic for these keys).
	if len(keysPerShard) != shardCount {
		t.Fatalf("test setup: keys hashed to %d shards, want %d", len(keysPerShard), shardCount)
	}

	// Evict 4 times; we should observe evictions from at least 2 distinct shards
	// (round-robin should not always drain the same shard).
	evictedShards := make(map[uint32]struct{})

	for range 4 {
		key, ok := s.Evict()
		if !ok {
			break
		}

		evictedShards[cachev2.Hash(key)&uint32(shardCount-1)] = struct{}{}
	}

	if len(evictedShards) < 2 {
		t.Errorf("Evict drained only %d shards in 4 calls; round-robin not distributing", len(evictedShards))
	}
}

// TestSharded_EvictReturnsFalseWhenAllEmpty covers the all-empty corner.
func TestSharded_EvictReturnsFalseWhenAllEmpty(t *testing.T) {
	t.Parallel()

	s, err := NewSharded("lru", 32, 8)
	if err != nil {
		t.Fatalf("NewSharded: %v", err)
	}

	if _, ok := s.Evict(); ok {
		t.Errorf("Evict on empty Sharded returned ok=true")
	}
}

// TestSharded_ConcurrentSetAndEvict is the race-safety smoke test.
func TestSharded_ConcurrentSetAndEvict(t *testing.T) {
	t.Parallel()

	s, err := NewSharded("lru", 1024, 32)
	if err != nil {
		t.Fatalf("NewSharded: %v", err)
	}

	const writers = 8

	const perWriter = 1000

	var wg sync.WaitGroup

	for w := range writers {
		wg.Go(func() {
			for i := range perWriter {
				key := "k" + strconv.Itoa(w*perWriter+i)

				s.Set(key, i)

				if i%4 == 0 {
					_, _ = s.Evict()
				}
			}
		})
	}

	wg.Wait()
}
