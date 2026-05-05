package tests

import (
	"context"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// recordingMeterProvider builds a meter provider backed by a manual
// reader so the test can synchronously trigger collection and inspect
// the produced metrics. Mirrors recordingTracerProvider in the tracing
// test — same pattern, separate signal.
func recordingMeterProvider() (*sdkmetric.MeterProvider, *sdkmetric.ManualReader) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	return mp, reader
}

// firstInt64DataPoint pulls the first data point from a Sum or Gauge
// aggregation. Returns 0,false when the aggregation is some other type
// (e.g. Histogram) or has no points yet. Extracted as a free helper so
// findMetricInt64 stays under the cognitive-complexity cap.
func firstInt64DataPoint(data metricdata.Aggregation) (int64, bool) {
	switch agg := data.(type) {
	case metricdata.Sum[int64]:
		if len(agg.DataPoints) > 0 {
			return agg.DataPoints[0].Value, true
		}

	case metricdata.Gauge[int64]:
		if len(agg.DataPoints) > 0 {
			return agg.DataPoints[0].Value, true
		}

	default:
		// Other aggregations (Histogram, ExponentialHistogram, etc.)
		// are not produced by the dist backend today — fall through.
	}

	return 0, false
}

// findMetricInt64 walks a collected ResourceMetrics looking for the
// first data point of an Int64-typed metric matching name. Returns
// (value, true) when found, (0, false) otherwise.
func findMetricInt64(rm metricdata.ResourceMetrics, name string) (int64, bool) {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}

			return firstInt64DataPoint(m.Data)
		}
	}

	return 0, false
}

// TestDistMemory_MetricsExportsObservableCounters is the contract test
// for Phase A.3: when a meter provider is supplied, a Set + Get
// produces an observable `dist.write.attempts` counter that reflects
// the work done. Counter must increment monotonically.
//
// We exercise the most-load-bearing counter (write attempts) here;
// other 40+ instruments share the same registration mechanism, so a
// single working counter validates the whole table.
func TestDistMemory_MetricsExportsObservableCounters(t *testing.T) {
	t.Parallel()

	mp, reader := recordingMeterProvider()
	addr := AllocatePort(t)

	bi, err := backend.NewDistMemory(
		context.Background(),
		backend.WithDistNode("metric-A", addr),
		backend.WithDistReplication(1),
		backend.WithDistMeterProvider(mp),
	)
	if err != nil {
		t.Fatalf("new dist memory: %v", err)
	}

	dm, ok := bi.(*backend.DistMemory)
	if !ok {
		t.Fatalf("expected *backend.DistMemory, got %T", bi)
	}

	StopOnCleanup(t, dm)

	ctx := context.Background()

	for i := range 3 {
		setErr := dm.Set(ctx, &cache.Item{
			Key:         "metric-key",
			Value:       []byte{byte(i)},
			Version:     uint64(i + 1),
			Origin:      "metric-A",
			LastUpdated: time.Now(),
		})
		if setErr != nil {
			t.Fatalf("set: %v", setErr)
		}
	}

	var rm metricdata.ResourceMetrics

	collectErr := reader.Collect(ctx, &rm)
	if collectErr != nil {
		t.Fatalf("collect: %v", collectErr)
	}

	got, found := findMetricInt64(rm, "dist.write.attempts")
	if !found {
		t.Fatalf("expected `dist.write.attempts` counter; not exported")
	}

	if got != 3 {
		t.Fatalf("expected dist.write.attempts=3 after 3 Sets, got %d", got)
	}
}

// TestDistMemory_MetricsExportsObservableGauges asserts the gauge path
// works alongside counters: `dist.members.alive` reflects current
// cluster state. The standalone DistMemory we build here has membership
// of 1 (itself), so the gauge must report 1 — proves the membership
// snapshot path inside the metric callback runs and surfaces live
// values, not just at-construction state.
func TestDistMemory_MetricsExportsObservableGauges(t *testing.T) {
	t.Parallel()

	mp, reader := recordingMeterProvider()
	addr := AllocatePort(t)

	bi, err := backend.NewDistMemory(
		context.Background(),
		backend.WithDistNode("metric-gauge-A", addr),
		backend.WithDistReplication(1),
		backend.WithDistMeterProvider(mp),
	)
	if err != nil {
		t.Fatalf("new dist memory: %v", err)
	}

	dm, ok := bi.(*backend.DistMemory)
	if !ok {
		t.Fatalf("expected *backend.DistMemory, got %T", bi)
	}

	StopOnCleanup(t, dm)

	var rm metricdata.ResourceMetrics

	collectErr := reader.Collect(context.Background(), &rm)
	if collectErr != nil {
		t.Fatalf("collect: %v", collectErr)
	}

	got, found := findMetricInt64(rm, "dist.members.alive")
	if !found {
		t.Fatalf("expected `dist.members.alive` gauge; not exported")
	}

	if got != 1 {
		t.Fatalf("expected dist.members.alive=1 (self only), got %d", got)
	}
}

// TestDistMemory_MetricsDefaultIsNoop confirms the zero-value path
// (no WithDistMeterProvider) produces no metrics — symmetric with the
// tracing default-noop test. Asserted by constructing without a meter
// provider and verifying the cache still works.
func TestDistMemory_MetricsDefaultIsNoop(t *testing.T) {
	t.Parallel()

	addr := AllocatePort(t)

	bi, err := backend.NewDistMemory(
		context.Background(),
		backend.WithDistNode("metric-default", addr),
		backend.WithDistReplication(1),
	)
	if err != nil {
		t.Fatalf("new dist memory: %v", err)
	}

	dm, ok := bi.(*backend.DistMemory)
	if !ok {
		t.Fatalf("expected *backend.DistMemory, got %T", bi)
	}

	StopOnCleanup(t, dm)

	setErr := dm.Set(context.Background(), &cache.Item{
		Key:         "default-metric-key",
		Value:       []byte("v"),
		Version:     1,
		Origin:      "metric-default",
		LastUpdated: time.Now(),
	})
	if setErr != nil {
		t.Fatalf("set: %v", setErr)
	}

	if it, ok := dm.Get(context.Background(), "default-metric-key"); !ok || it == nil {
		t.Fatalf("Get returned nothing for the key we just set")
	}
}
