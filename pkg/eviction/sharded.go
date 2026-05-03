package eviction

import (
	"sync/atomic"

	"github.com/hyp3rd/ewrap"
	"github.com/hyp3rd/sectools/pkg/converters"

	"github.com/hyp3rd/hypercache/internal/sentinel"
	cachev2 "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// Sharded wraps shardCount independent IAlgorithm instances and routes each
// key to one shard via the same hash that pkg/cache/v2.ConcurrentMap uses.
// This eliminates the global mutex contention of single-instance algorithms
// (LRU/LFU/Clock) by replacing it with shardCount distinct mutexes.
//
// Behavior change vs unsharded: items are evicted within their own shard, not
// in strict global LRU/LFU order. Total capacity is honored (sum of per-shard
// capacities). For users that need strict global-order eviction, construct
// the algorithm directly (or use shardCount=1 via WithEvictionShardCount(1)).
type Sharded struct {
	shards      []IAlgorithm
	mask        uint32
	evictCursor atomic.Uint32 // round-robin cursor for Evict
}

// NewSharded constructs a Sharded eviction wrapper. shardCount must be a
// power of two so the hash can be masked with `& (shardCount-1)`. Each
// underlying shard receives capacity ceil(totalCapacity / shardCount).
//
// algorithmName is one of the registered algorithms (lru, lfu, clock,
// cawolfu). Each shard is an independent instance; they do not share state.
func NewSharded(algorithmName string, totalCapacity, shardCount int) (*Sharded, error) {
	if shardCount <= 0 || shardCount&(shardCount-1) != 0 {
		return nil, ewrap.Wrapf(sentinel.ErrInvalidCapacity, "shardCount %d must be a positive power of two", shardCount)
	}

	if totalCapacity < 0 {
		return nil, sentinel.ErrInvalidCapacity
	}

	perShard := (totalCapacity + shardCount - 1) / shardCount

	shards := make([]IAlgorithm, shardCount)

	for i := range shardCount {
		shard, err := NewEvictionAlgorithm(algorithmName, perShard)
		if err != nil {
			return nil, ewrap.Wrapf(err, "shard %d", i)
		}

		shards[i] = shard
	}

	// shardCount validated power-of-two above
	mask, err := converters.ToUint32(shardCount - 1)
	if err != nil {
		return nil, ewrap.Wrapf(err, "shardCount %d", shardCount)
	}

	return &Sharded{
		shards: shards,
		mask:   mask,
	}, nil
}

// Set forwards to the key's shard.
func (s *Sharded) Set(key string, value any) {
	s.shardFor(key).Set(key, value)
}

// Get forwards to the key's shard.
func (s *Sharded) Get(key string) (any, bool) {
	return s.shardFor(key).Get(key)
}

// Delete forwards to the key's shard.
func (s *Sharded) Delete(key string) {
	s.shardFor(key).Delete(key)
}

// Evict picks a shard via round-robin and evicts from it. If that shard is
// empty, the next shard is tried until a non-empty one is found or all
// shards have been visited. Returns ("", false) only when all shards are
// empty.
//
// Round-robin distributes eviction pressure across shards rather than
// always hitting the first non-empty one — relevant when one shard sees
// disproportionately fewer Set calls than others.
func (s *Sharded) Evict() (string, bool) {
	// len(s.shards) bounded by shardCount param
	n, err := converters.ToUint32(len(s.shards))
	if err != nil {
		return "", false
	}

	start := s.evictCursor.Add(1) - 1

	for i := range n {
		idx := (start + i) & s.mask
		if key, ok := s.shards[idx].Evict(); ok {
			return key, true
		}
	}

	return "", false
}

// shardFor returns the shard responsible for key. Uses the same hash function
// as pkg/cache/v2.ConcurrentMap so a key's data shard and eviction shard map
// to the same logical position (cache-locality on Set).
func (s *Sharded) shardFor(key string) IAlgorithm {
	return s.shards[cachev2.Hash(key)&s.mask]
}
