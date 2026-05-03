package v2

import (
	"github.com/cespare/xxhash/v2"
	"github.com/hyp3rd/sectools/pkg/converters"
)

// Hash returns a 32-bit hash of key derived from xxhash64. Exported so
// other packages (e.g. the sharded eviction wrapper) can use the same
// hash as ConcurrentMap and route the same key to the same logical shard
// — preserving cache-locality when the data shard and eviction shard
// have different counts.
//
// xxhash is already a direct dependency for cluster/ring.go consistent
// hashing; consolidating here removes the inlined FNV-1a implementation
// and gives ~1-3% speedup for keys longer than ~8 bytes plus better
// avalanche characteristics. Callers should treat the return value as
// opaque.
//
// We fold the 64-bit xxhash output into 32 bits via XOR of the high and
// low halves — cheap, preserves all entropy, and matches what Go's
// standard maphash does when callers want a 32-bit slot index.
func Hash(key string) uint32 {
	const (
		// hashFoldShift is the bit-shift used to fold the upper 32 bits of
		// the xxhash64 output onto the lower 32 bits via XOR.
		hashFoldShift = 32
		// lower32Mask zeroes the upper 32 bits of a uint64 so the result
		// fits in uint32. Without it, the XOR `h ^ (h >> 32)` keeps
		// h's upper 32 bits intact in the high half of the uint64, and
		// converters.ToUint32 (correctly) rejects it.
		lower32Mask uint64 = 0xFFFFFFFF
	)

	h := xxhash.Sum64String(key)
	folded := (h ^ (h >> hashFoldShift)) & lower32Mask

	res, err := converters.ToUint32(folded)
	if err != nil {
		// Unreachable: folded is masked to MaxUint32 above.
		panic("hash fold overflow: " + err.Error())
	}

	return res
}
