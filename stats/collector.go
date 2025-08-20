package stats

import (
	"github.com/hyp3rd/ewrap"

	"github.com/hyp3rd/hypercache/sentinel"
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

// CollectorRegistry manages stats collector constructors.
type CollectorRegistry struct {
	collectors map[string]func() (ICollector, error)
}

// NewCollectorRegistry creates a new collector registry with default collectors pre-registered.
func NewCollectorRegistry() *CollectorRegistry {
	registry := &CollectorRegistry{
		collectors: make(map[string]func() (ICollector, error)),
	}
	// Register the default collector
	registry.Register("default", func() (ICollector, error) {
		collector := NewHistogramStatsCollector()
		if collector == nil {
			return nil, ewrap.Wrap(sentinel.ErrStatsCollectorNotFound, "default")
		}

		return collector, nil
	})

	return registry
}

// NewEmptyCollectorRegistry creates a new collector registry without default collectors.
// This is useful for testing or when you want to register only specific collectors.
func NewEmptyCollectorRegistry() *CollectorRegistry {
	return &CollectorRegistry{
		collectors: make(map[string]func() (ICollector, error)),
	}
}

// Register registers a new stats collector with the given name.
func (r *CollectorRegistry) Register(name string, createFunc func() (ICollector, error)) {
	r.collectors[name] = createFunc
}

// NewCollector creates a new stats collector.
func (r *CollectorRegistry) NewCollector(statsCollectorName string) (ICollector, error) {
	// Check the parameters.
	if statsCollectorName == "" {
		return nil, ewrap.Wrap(sentinel.ErrParamCannotBeEmpty, "statsCollectorName")
	}

	createFunc, ok := r.collectors[statsCollectorName]
	if !ok {
		return nil, ewrap.Wrap(sentinel.ErrStatsCollectorNotFound, statsCollectorName)
	}

	return createFunc()
}

// NewCollector creates a new stats collector using a new registry instance with default collectors.
// The statsCollectorName parameter is used to select the stats collector from the default collectors.
func NewCollector(statsCollectorName string) (ICollector, error) {
	registry := NewCollectorRegistry()

	return registry.NewCollector(statsCollectorName)
}
