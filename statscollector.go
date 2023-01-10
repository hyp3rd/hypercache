package hypercache

import (
	"fmt"

	"github.com/hyp3rd/hypercache/errors"
	"github.com/hyp3rd/hypercache/stats"
	"github.com/hyp3rd/hypercache/types"
)

// StatsCollector is an interface that defines the methods that a stats collector should implement.
type StatsCollector interface {
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
	GetStats() stats.Stats
}

// StatsCollectorRegistry holds the a registry of stats collectors.
var StatsCollectorRegistry = make(map[string]func() (StatsCollector, error))

// NewStatsCollector creates a new stats collector.
// The statsCollectorName parameter is used to select the stats collector from the registry.
func NewStatsCollector(statsCollectorName string) (StatsCollector, error) {
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

// RegisterStatsCollector registers a new stats collector with the given name.
func RegisterStatsCollector(name string, createFunc func() (StatsCollector, error)) {
	StatsCollectorRegistry[name] = createFunc
}

func init() {
	// Register the default stats collector.
	RegisterStatsCollector("default", func() (StatsCollector, error) {
		var err error
		collector := stats.NewHistogramStatsCollector()
		if collector == nil {
			err = fmt.Errorf("%s: %s", errors.ErrStatsCollectorNotFound, "default")
		}
		return stats.NewHistogramStatsCollector(), err
	})
}
