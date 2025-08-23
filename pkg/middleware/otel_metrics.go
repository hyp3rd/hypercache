package middleware

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/internal/telemetry/attrs"
	"github.com/hyp3rd/hypercache/pkg/backend"
	"github.com/hyp3rd/hypercache/pkg/cache"
	"github.com/hyp3rd/hypercache/pkg/stats"
)

// OTelMetricsMiddleware emits OpenTelemetry metrics for service methods.
type OTelMetricsMiddleware struct {
	next  hypercache.Service
	meter metric.Meter

	// instruments
	calls     metric.Int64Counter
	durations metric.Float64Histogram
}

// NewOTelMetricsMiddleware constructs a metrics middleware using the provided meter.
func NewOTelMetricsMiddleware(next hypercache.Service, meter metric.Meter) (hypercache.Service, error) {
	calls, err := meter.Int64Counter("hypercache.calls")
	if err != nil {
		return nil, fmt.Errorf("create counter: %w", err)
	}

	durations, err := meter.Float64Histogram("hypercache.duration.ms")
	if err != nil {
		return nil, fmt.Errorf("create histogram: %w", err)
	}

	return &OTelMetricsMiddleware{next: next, meter: meter, calls: calls, durations: durations}, nil
}

// Get implements Service.Get with metrics.
func (mw *OTelMetricsMiddleware) Get(ctx context.Context, key string) (any, bool) {
	start := time.Now()
	v, ok := mw.next.Get(ctx, key)
	mw.rec(ctx, "Get", start, attribute.Int(attrs.AttrKeyLength, len(key)), attribute.Bool("hit", ok))

	return v, ok
}

// Set implements Service.Set with metrics.
func (mw *OTelMetricsMiddleware) Set(ctx context.Context, key string, value any, expiration time.Duration) error {
	start := time.Now()
	err := mw.next.Set(ctx, key, value, expiration)
	mw.rec(ctx, "Set", start, attribute.Int(attrs.AttrKeyLength, len(key)))

	return err
}

// GetOrSet implements Service.GetOrSet with metrics.
func (mw *OTelMetricsMiddleware) GetOrSet(ctx context.Context, key string, value any, expiration time.Duration) (any, error) {
	start := time.Now()
	v, err := mw.next.GetOrSet(ctx, key, value, expiration)
	mw.rec(ctx, "GetOrSet", start, attribute.Int(attrs.AttrKeyLength, len(key)))

	return v, err
}

// GetWithInfo implements Service.GetWithInfo with metrics.
func (mw *OTelMetricsMiddleware) GetWithInfo(ctx context.Context, key string) (*cache.Item, bool) {
	start := time.Now()
	it, ok := mw.next.GetWithInfo(ctx, key)
	mw.rec(ctx, "GetWithInfo", start, attribute.Int(attrs.AttrKeyLength, len(key)), attribute.Bool("hit", ok))

	return it, ok
}

// GetMultiple implements Service.GetMultiple with metrics.
func (mw *OTelMetricsMiddleware) GetMultiple(ctx context.Context, keys ...string) (map[string]any, map[string]error) {
	start := time.Now()
	res, failed := mw.next.GetMultiple(ctx, keys...)
	mw.rec(
		ctx,
		"GetMultiple",
		start,
		attribute.Int(attrs.AttrKeysCount, len(keys)),
		attribute.Int(attrs.AttrResultCount, len(res)),
		attribute.Int(attrs.AttrFailedCount, len(failed)),
	)

	return res, failed
}

// List implements Service.List with metrics.
func (mw *OTelMetricsMiddleware) List(ctx context.Context, filters ...backend.IFilter) ([]*cache.Item, error) {
	start := time.Now()
	items, err := mw.next.List(ctx, filters...)

	n := 0
	if items != nil {
		n = len(items)
	}

	mw.rec(ctx, "List", start, attribute.Int("items.count", n))

	return items, err
}

// Remove implements Service.Remove with metrics.
func (mw *OTelMetricsMiddleware) Remove(ctx context.Context, keys ...string) error {
	start := time.Now()
	err := mw.next.Remove(ctx, keys...)
	mw.rec(ctx, "Remove", start, attribute.Int("keys.count", len(keys)))

	return err
}

// Clear implements Service.Clear with metrics.
func (mw *OTelMetricsMiddleware) Clear(ctx context.Context) error {
	start := time.Now()
	err := mw.next.Clear(ctx)
	mw.rec(ctx, "Clear", start)

	return err
}

// Capacity returns cache capacity.
func (mw *OTelMetricsMiddleware) Capacity() int { return mw.next.Capacity() }

// Allocation returns allocated size.
func (mw *OTelMetricsMiddleware) Allocation() int64 { return mw.next.Allocation() }

// Count returns items count.
func (mw *OTelMetricsMiddleware) Count(ctx context.Context) int { return mw.next.Count(ctx) }

// TriggerEviction triggers eviction.
func (mw *OTelMetricsMiddleware) TriggerEviction(ctx context.Context) { mw.next.TriggerEviction(ctx) }

// Stop stops the underlying service.
func (mw *OTelMetricsMiddleware) Stop(ctx context.Context) error { return mw.next.Stop(ctx) }

// GetStats returns stats.
func (mw *OTelMetricsMiddleware) GetStats() stats.Stats { return mw.next.GetStats() }

// rec records call count and duration with attributes.
func (mw *OTelMetricsMiddleware) rec(ctx context.Context, method string, start time.Time, attributes ...attribute.KeyValue) {
	base := []attribute.KeyValue{attribute.String("method", method)}
	if len(attributes) > 0 {
		base = append(base, attributes...)
	}

	mw.calls.Add(ctx, 1, metric.WithAttributes(base...))
	mw.durations.Record(ctx, float64(time.Since(start).Milliseconds()), metric.WithAttributes(base...))
}
