package stats

import (
	"fmt"

	"github.com/hyp3rd/hypercache/errors"
	"github.com/hyp3rd/hypercache/types"
)

// Collector is an interface that defines the methods that a stats collector should implement.
type Collector interface {
	// Incr increments the count of a statistic by the given value.
	Incr(stat types.Stat, value int64)
	// Decr decrements the count of a statistic by the given value.
	Decr(stat types.Stat, value int64)
	// Timing records the time it took for an event to occur.
	Timing(stat types.Stat, value int64)
	// Gauge records the current value of a statistic.
	Gauge(stat types.Stat, value int64)
	// Histogram records the statistical distribution of a set of values.
	Histogram(stat types.Stat, value int64)
	// GetStats returns the collected statistics.
	GetStats() Stats
}

// StatsCollectorRegistry holds the a registry of stats collectors.
var StatsCollectorRegistry = make(map[string]func() (Collector, error))

// NewCollector creates a new stats collector.
// The statsCollectorName parameter is used to select the stats collector from the registry.
func NewCollector(statsCollectorName string) (Collector, error) {
	// Check the parameters.
	if statsCollectorName == "" {
		return nil, fmt.Errorf("%s: %s", errors.ErrParamCannotBeEmpty, "statsCollectorName")
	}

	createFunc, ok := StatsCollectorRegistry[statsCollectorName]
	if !ok {
		return nil, fmt.Errorf("%s: %s", errors.ErrStatsCollectorNotFound, statsCollectorName)
	}

	return createFunc()
}

// RegisterCollector registers a new stats collector with the given name.
func RegisterCollector(name string, createFunc func() (Collector, error)) {
	StatsCollectorRegistry[name] = createFunc
}

func init() {
	// Register the default stats collector.
	RegisterCollector("default", func() (Collector, error) {
		var err error
		collector := NewHistogramStatsCollector()
		if collector == nil {
			err = fmt.Errorf("%s: %s", errors.ErrStatsCollectorNotFound, "default")
		}
		return NewHistogramStatsCollector(), err
	})
}
