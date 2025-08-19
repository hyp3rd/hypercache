package stats

import (
	"github.com/hyp3rd/ewrap"

	"github.com/hyp3rd/hypercache/errors"
	"github.com/hyp3rd/hypercache/types"
)

// ICollector is an interface that defines the methods that a stats collector should implement.
type ICollector interface {
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
var StatsCollectorRegistry = make(map[string]func() (ICollector, error))

// NewCollector creates a new stats collector.
// The statsCollectorName parameter is used to select the stats collector from the registry.
func NewCollector(statsCollectorName string) (ICollector, error) {
	// Check the parameters.
	if statsCollectorName == "" {
		return nil, ewrap.Wrap(errors.ErrParamCannotBeEmpty, "statsCollectorName")
	}

	createFunc, ok := StatsCollectorRegistry[statsCollectorName]
	if !ok {
		return nil, ewrap.Wrap(errors.ErrStatsCollectorNotFound, statsCollectorName)
	}

	return createFunc()
}

// RegisterCollector registers a new stats collector with the given name.
func RegisterCollector(name string, createFunc func() (ICollector, error)) {
	StatsCollectorRegistry[name] = createFunc
}

func init() {
	// Register the default stats collector.
	RegisterCollector("default", func() (ICollector, error) {
		var err error

		collector := NewHistogramStatsCollector()
		if collector == nil {
			err = ewrap.Wrap(errors.ErrStatsCollectorNotFound, "default")
		}

		return NewHistogramStatsCollector(), err
	})
}
