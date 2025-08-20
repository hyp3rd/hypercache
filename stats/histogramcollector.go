package stats

import (
	"math"
	"slices"
	"sort"
	"sync"

	"github.com/hyp3rd/hypercache/types"
)

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
func (c *HistogramStatsCollector) Incr(stat types.Stat, value int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.stats[stat.String()] = append(c.stats[stat.String()], value)
}

// Decr decrements the count of a statistic by the given value.
func (c *HistogramStatsCollector) Decr(stat types.Stat, value int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.stats[stat.String()] = append(c.stats[stat.String()], -value)
}

// Timing records the time it took for an event to occur.
func (c *HistogramStatsCollector) Timing(stat types.Stat, value int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.stats[stat.String()] = append(c.stats[stat.String()], value)
}

// Gauge records the current value of a statistic.
func (c *HistogramStatsCollector) Gauge(stat types.Stat, value int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.stats[stat.String()] = append(c.stats[stat.String()], value)
}

// Histogram records the statistical distribution of a set of values.
func (c *HistogramStatsCollector) Histogram(stat types.Stat, value int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.stats[stat.String()] = append(c.stats[stat.String()], value)
}

// Mean returns the mean value of a statistic.
func (c *HistogramStatsCollector) Mean(stat types.Stat) float64 {
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
func (c *HistogramStatsCollector) Median(stat types.Stat) float64 {
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
func (c *HistogramStatsCollector) Percentile(stat types.Stat, percentile float64) float64 {
	values := c.stats[stat.String()]
	if len(values) == 0 {
		return 0
	}

	slices.Sort(values)
	index := int(float64(len(values)) * percentile)

	return float64(values[index])
}

// GetStats returns the stats collected by the stats collector.
// It calculates the mean, median, min, max, count, sum, and variance for each stat.
// It returns a map where the keys are the stat names and the values are the stat values.
func (c *HistogramStatsCollector) GetStats() Stats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	stats := make(Stats)
	for stat, values := range c.stats {
		mean := c.Mean(types.Stat(stat))
		median := c.Median(types.Stat(stat))

		slices.Sort(values)

		minVal := values[0]
		maxVal := values[len(values)-1]

		stats[stat] = &Stat{
			Mean:     mean,
			Median:   median,
			Min:      minVal,
			Max:      maxVal,
			Values:   values,
			Count:    len(values),
			Sum:      sum(values),
			Variance: variance(values, mean),
		}
	}

	return stats
}

// sum returns the sum of a set of values.
func sum(values []int64) int64 {
	var sum int64
	for _, value := range values {
		sum += value
	}

	return sum
}

// variance returns the variance of a set of values.
func variance(values []int64, mean float64) float64 {
	if len(values) == 0 {
		return 0
	}

	var variance float64
	for _, value := range values {
		variance += math.Pow(float64(value)-mean, 2)
	}

	return variance / float64(len(values))
}
