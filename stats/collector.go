package stats

import (
	"sort"
	"sync"
)

// Stat defines a type Stat as an alias for string, and five constants of type Stat.
// The constants are used to represent different stat values that can be collected by the stats collector.
type Stat string

const (
	// StatIncr represent a stat that should be incremented
	StatIncr Stat = "incr"
	// StatDecr represent a stat that should be decremented
	StatDecr Stat = "decr"
	// StatTiming represent a stat that represents the time it takes for an event to occur
	StatTiming Stat = "timing"
	// StatGauge represent a stat that represents the current value of a statistic
	StatGauge Stat = "gauge"
	// StatHistogram represent a stat that represents the statistical distribution of a set of values
	StatHistogram Stat = "histogram"
)

// String returns the string representation of a Stat.
func (s Stat) String() string {
	return string(s)
}

// HistogramStatsCollector is a stats collector that collects histogram stats.
type HistogramStatsCollector struct {
	mu    sync.RWMutex // mutex to protect concurrent access to the stats
	stats map[string][]int64
}

// NewHistogramStatsCollector creates a new histogram stats collector.
func NewHistogramStatsCollector() *HistogramStatsCollector {
	return &HistogramStatsCollector{
		stats: make(map[string][]int64),
	}
}

// Incr increments the count of a statistic by the given value.
func (c *HistogramStatsCollector) Incr(stat Stat, value int64) {
	// Lock the cache's mutex to ensure thread-safety
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stats[stat.String()] = append(c.stats[stat.String()], value)
}

// Decr decrements the count of a statistic by the given value.
func (c *HistogramStatsCollector) Decr(stat Stat, value int64) {
	// Lock the cache's mutex to ensure thread-safety
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stats[stat.String()] = append(c.stats[stat.String()], -value)
}

// Timing records the time it took for an event to occur.
func (c *HistogramStatsCollector) Timing(stat Stat, value int64) {
	// Lock the cache's mutex to ensure thread-safety
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stats[stat.String()] = append(c.stats[stat.String()], value)
}

// Gauge records the current value of a statistic.
func (c *HistogramStatsCollector) Gauge(stat Stat, value int64) {
	// Lock the cache's mutex to ensure thread-safety
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stats[stat.String()] = append(c.stats[stat.String()], value)
}

// Histogram records the statistical distribution of a set of values.
func (c *HistogramStatsCollector) Histogram(stat Stat, value int64) {
	// Lock the cache's mutex to ensure thread-safety
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stats[stat.String()] = append(c.stats[stat.String()], value)
}

// Mean returns the mean value of a statistic.
func (c *HistogramStatsCollector) Mean(stat Stat) float64 {
	values := c.stats[stat.String()]
	if len(values) == 0 {
		return 0
	}
	var sum int64
	for _, value := range values {
		sum += value
	}
	return float64(sum) / float64(len(values))
}

// Median returns the median value of a statistic.
func (c *HistogramStatsCollector) Median(stat Stat) float64 {
	values := c.stats[stat.String()]
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	mid := len(values) / 2
	if len(values)%2 == 0 {
		return float64(values[mid-1]+values[mid]) / 2
	}
	return float64(values[mid])
}

// Percentile returns the pth percentile value of a statistic.
func (c *HistogramStatsCollector) Percentile(stat Stat, p float64) float64 {
	values := c.stats[stat.String()]
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	index := int(float64(len(values)) * p)
	return float64(values[index])
}
