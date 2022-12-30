package hypercache

// StatsCollector is an interface for collecting cache statistics.
// It has four methods for incrementing the number of cache hits, misses, evictions, and expirations, and a method for getting the cache statistics.
// It is used by the HyperCache struct to allow users to collect cache statistics using their own implementation.
type StatsCollector interface {
	IncrementHits()        // IncrementHits increments the number of cache hits.
	IncrementMisses()      // IncrementMisses increments the number of cache misses.
	IncrementEvictions()   // IncrementEvictions increments the number of cache evictions.
	IncrementExpirations() // IncrementExpirations increments the number of cache expirations.
	GetStats() any         // GetStats returns the cache statistics.
}
