package v2

// Hash returns a 32-bit FNV-1a hash of key. Exported so other packages (e.g.
// the sharded eviction wrapper) can use the same hash as ConcurrentMap and
// route the same key to the same logical shard — preserving cache-locality
// when the data shard and eviction shard have different counts.
//
// Step 4 of the modernization will replace this with xxhash.Sum64String for
// faster hashing and better distribution. Callers should treat the return
// value as opaque.
func Hash(key string) uint32 {
	const (
		fnvOffset32 = 2166136261
		fnvPrime32  = 16777619
	)

	var sum uint32 = fnvOffset32

	for i := range key { // Go 1.22+ integer range over string indices
		sum ^= uint32(key[i])

		sum *= fnvPrime32
	}

	return sum
}
