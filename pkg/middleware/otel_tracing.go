// Package middleware contains service middlewares for hypercache.
package middleware

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/internal/telemetry/attrs"
	"github.com/hyp3rd/hypercache/pkg/backend"
	"github.com/hyp3rd/hypercache/pkg/cache"
	"github.com/hyp3rd/hypercache/pkg/stats"
)

// OTelTracingMiddleware wraps hypercache.Service methods with OpenTelemetry spans.
type OTelTracingMiddleware struct {
	next   hypercache.Service
	tracer trace.Tracer
	// static attributes applied to all spans
	commonAttrs []attribute.KeyValue
}

// OTelTracingOption allows configuring the tracing middleware.
type OTelTracingOption func(*OTelTracingMiddleware)

// WithCommonAttributes sets attributes applied to all spans.
func WithCommonAttributes(attributes ...attribute.KeyValue) OTelTracingOption {
	return func(m *OTelTracingMiddleware) { m.commonAttrs = append(m.commonAttrs, attributes...) }
}

// NewOTelTracingMiddleware creates a tracing middleware.
func NewOTelTracingMiddleware(next hypercache.Service, tracer trace.Tracer, opts ...OTelTracingOption) hypercache.Service {
	mw := &OTelTracingMiddleware{next: next, tracer: tracer}
	for _, o := range opts {
		o(mw)
	}

	return mw
}

// Get implements Service.Get with tracing.
func (mw OTelTracingMiddleware) Get(ctx context.Context, key string) (any, bool) {
	ctx, span := mw.startSpan(ctx, "hypercache.Get", attribute.Int(attrs.AttrKeyLength, len(key)))
	defer span.End()

	v, ok := mw.next.Get(ctx, key)
	span.SetAttributes(attribute.Bool("hit", ok))

	return v, ok
}

// Set implements Service.Set with tracing.
func (mw OTelTracingMiddleware) Set(ctx context.Context, key string, value any, expiration time.Duration) error {
	ctx, span := mw.startSpan(
		ctx, "hypercache.Set",
		attribute.Int(attrs.AttrKeyLength, len(key)),
		attribute.Int64(attrs.AttrExpirationMS, expiration.Milliseconds()))
	defer span.End()

	err := mw.next.Set(ctx, key, value, expiration)
	if err != nil {
		span.RecordError(err)
	}

	return err
}

// GetOrSet implements Service.GetOrSet with tracing.
func (mw OTelTracingMiddleware) GetOrSet(ctx context.Context, key string, value any, expiration time.Duration) (any, error) {
	ctx, span := mw.startSpan(
		ctx, "hypercache.GetOrSet",
		attribute.Int(attrs.AttrKeyLength, len(key)),
		attribute.Int64(attrs.AttrExpirationMS, expiration.Milliseconds()))
	defer span.End()

	v, err := mw.next.GetOrSet(ctx, key, value, expiration)
	if err != nil {
		span.RecordError(err)
	}

	return v, err
}

// GetWithInfo implements Service.GetWithInfo with tracing.
func (mw OTelTracingMiddleware) GetWithInfo(ctx context.Context, key string) (*cache.Item, bool) {
	ctx, span := mw.startSpan(ctx, "hypercache.GetWithInfo", attribute.Int(attrs.AttrKeyLength, len(key)))
	defer span.End()

	it, ok := mw.next.GetWithInfo(ctx, key)
	span.SetAttributes(attribute.Bool("hit", ok))

	return it, ok
}

// GetMultiple implements Service.GetMultiple with tracing.
func (mw OTelTracingMiddleware) GetMultiple(ctx context.Context, keys ...string) (map[string]any, map[string]error) {
	ctx, span := mw.startSpan(ctx, "hypercache.GetMultiple", attribute.Int("keys.count", len(keys)))
	defer span.End()

	res, failed := mw.next.GetMultiple(ctx, keys...)
	span.SetAttributes(attribute.Int("result.count", len(res)), attribute.Int("failed.count", len(failed)))

	return res, failed
}

// List implements Service.List with tracing.
func (mw OTelTracingMiddleware) List(ctx context.Context, filters ...backend.IFilter) ([]*cache.Item, error) {
	ctx, span := mw.startSpan(ctx, "hypercache.List")
	defer span.End()

	items, err := mw.next.List(ctx, filters...)
	if err != nil {
		span.RecordError(err)
	}

	if items != nil {
		span.SetAttributes(attribute.Int("items.count", len(items)))
	}

	return items, err
}

// Remove implements Service.Remove with tracing.
func (mw OTelTracingMiddleware) Remove(ctx context.Context, keys ...string) error {
	ctx, span := mw.startSpan(ctx, "hypercache.Remove", attribute.Int("keys.count", len(keys)))
	defer span.End()

	err := mw.next.Remove(ctx, keys...)
	if err != nil {
		span.RecordError(err)
	}

	return err
}

// Clear implements Service.Clear with tracing.
func (mw OTelTracingMiddleware) Clear(ctx context.Context) error {
	ctx, span := mw.startSpan(ctx, "hypercache.Clear")
	defer span.End()

	err := mw.next.Clear(ctx)
	if err != nil {
		span.RecordError(err)
	}

	return err
}

// Capacity returns cache capacity.
func (mw OTelTracingMiddleware) Capacity() int { return mw.next.Capacity() }

// Allocation returns allocated size.
func (mw OTelTracingMiddleware) Allocation() int64 { return mw.next.Allocation() }

// Count returns items count.
func (mw OTelTracingMiddleware) Count(ctx context.Context) int { return mw.next.Count(ctx) }

// TriggerEviction triggers eviction with a span.
func (mw OTelTracingMiddleware) TriggerEviction(ctx context.Context) {
	_, span := mw.startSpan(ctx, "hypercache.TriggerEviction")
	defer span.End()

	mw.next.TriggerEviction(ctx)
}

// Stop stops the service with a span.
func (mw OTelTracingMiddleware) Stop(ctx context.Context) error {
	_, span := mw.startSpan(ctx, "hypercache.Stop")
	defer span.End()

	return mw.next.Stop(ctx)
}

// GetStats returns stats.
func (mw OTelTracingMiddleware) GetStats() stats.Stats { return mw.next.GetStats() }

// startSpan starts a span with common and provided attributes.
func (mw OTelTracingMiddleware) startSpan(ctx context.Context, name string, attributes ...attribute.KeyValue) (context.Context, trace.Span) {
	ctx, span := mw.tracer.Start(ctx, name, trace.WithSpanKind(trace.SpanKindInternal))
	if len(mw.commonAttrs) > 0 {
		span.SetAttributes(mw.commonAttrs...)
	}

	if len(attributes) > 0 {
		span.SetAttributes(attributes...)
	}

	return ctx, span
}
