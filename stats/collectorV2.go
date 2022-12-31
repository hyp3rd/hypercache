package stats

import "sort"

type HistogramStatsCollector struct {
	stats map[string][]int64
}

// NewHistogramStatsCollector creates a new histogram stats collector.
func NewHistogramStatsCollector() *HistogramStatsCollector {
	return &HistogramStatsCollector{
		stats: make(map[string][]int64),
	}
}

// Incr increments the count of a statistic by the given value.
func (c *HistogramStatsCollector) Incr(stat string, value int64) {
	c.stats[stat] = append(c.stats[stat], value)
}

// Decr decrements the count of a statistic by the given value.
func (c *HistogramStatsCollector) Decr(stat string, value int64) {
	c.stats[stat] = append(c.stats[stat], -value)
}

// Timing records the time it took for an event to occur.
func (c *HistogramStatsCollector) Timing(stat string, value int64) {
	c.stats[stat] = append(c.stats[stat], value)
}

// Gauge records the current value of a statistic.
func (c *HistogramStatsCollector) Gauge(stat string, value int64) {
	c.stats[stat] = append(c.stats[stat], value)
}

// Histogram records the statistical distribution of a set of values.
func (c *HistogramStatsCollector) Histogram(stat string, value int64) {
	c.stats[stat] = append(c.stats[stat], value)
}

// Mean returns the mean value of a statistic.
func (c *HistogramStatsCollector) Mean(stat string) float64 {
	values := c.stats[stat]
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
func (c *HistogramStatsCollector) Median(stat string) float64 {
	values := c.stats[stat]
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
func (c *HistogramStatsCollector) Percentile(stat string, p float64) float64 {
	values := c.stats[stat]
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	index := int(float64(len(values)) * p)
	return float64(values[index])
}
