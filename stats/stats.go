package stats

import "sync"

// Stats contains cache statistics.
type Stats struct {
	Hits        uint64 // number of cache hits
	Misses      uint64 // number of cache misses
	Evictions   uint64 // number of cache evictions
	Expirations uint64 // number of cache expirations
}

// Collector is a struct for collecting cache statistics.
type Collector struct {
	mu    sync.RWMutex // mutex to protect concurrent access to the stats
	stats Stats        // cache statistics
}

// NewCollector creates a new stats collector.
func NewCollector() *Collector {
	return &Collector{
		stats: Stats{},
	}
}

// IncrementHits increments the number of cache hits.
func (c *Collector) IncrementHits() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stats.Hits++
}

// IncrementMisses increments the number of cache misses.
func (c *Collector) IncrementMisses() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stats.Misses++
}

// IncrementEvictions increments the number of cache evictions.
func (c *Collector) IncrementEvictions() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stats.Evictions++
}

// IncrementExpirations increments the number of cache expirations.
func (c *Collector) IncrementExpirations() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stats.Expirations++
}

// GetStats returns the cache statistics.
func (c *Collector) GetStats() Stats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.stats
}
