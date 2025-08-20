// Package middleware provides various middleware implementations for the hypercache service.
// This package includes logging middleware that wraps the hypercache service to provide
// execution time logging and method call tracing for debugging and monitoring purposes.
package middleware

import (
	"context"
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/pkg/backend"
	"github.com/hyp3rd/hypercache/pkg/cache"
	"github.com/hyp3rd/hypercache/pkg/stats"
)

// Logger describes a logging interface allowing to implement different external, or custom logger.
// Tested with logrus, and Uber's Zap (high-performance), but should work with any other logger that matches the interface.
type Logger interface {
	Printf(format string, v ...any)
	// Errorf(format string, v ...any)
}

// LoggingMiddleware is a middleware that logs the time it takes to execute the next middleware.
// Must implement the hypercache.Service interface.
type LoggingMiddleware struct {
	next   hypercache.Service
	logger Logger
}

// NewLoggingMiddleware returns a new LoggingMiddleware.
func NewLoggingMiddleware(next hypercache.Service, logger Logger) hypercache.Service {
	return &LoggingMiddleware{next: next, logger: logger}
}

// Get logs the time it takes to execute the next middleware.
func (mw LoggingMiddleware) Get(ctx context.Context, key string) (any, bool) {
	defer func(begin time.Time) {
		mw.logger.Printf("method Get took: %s", time.Since(begin))
	}(time.Now())

	mw.logger.Printf("Get method called with key: %s", key)

	return mw.next.Get(ctx, key)
}

// Set logs the time it takes to execute the next middleware.
func (mw LoggingMiddleware) Set(ctx context.Context, key string, value any, expiration time.Duration) error {
	defer func(begin time.Time) {
		mw.logger.Printf("method Set took: %s", time.Since(begin))
	}(time.Now())

	mw.logger.Printf("Set method called with key: %s value: %s", key, value)

	return mw.next.Set(ctx, key, value, expiration)
}

// GetOrSet logs the time it takes to execute the next middleware.
func (mw LoggingMiddleware) GetOrSet(ctx context.Context, key string, value any, expiration time.Duration) (any, error) {
	defer func(begin time.Time) {
		mw.logger.Printf("method GetOrSet took: %s", time.Since(begin))
	}(time.Now())

	mw.logger.Printf("GetOrSet method invoked with key: %s value: %s", key, value)

	return mw.next.GetOrSet(ctx, key, value, expiration)
}

// GetWithInfo logs the time it takes to execute the next middleware.
func (mw LoggingMiddleware) GetWithInfo(ctx context.Context, key string) (*cache.Item, bool) {
	defer func(begin time.Time) {
		mw.logger.Printf("method GetWithInfo took: %s", time.Since(begin))
	}(time.Now())

	mw.logger.Printf("GetWithInfo method invoked with key: %s", key)

	return mw.next.GetWithInfo(ctx, key)
}

// GetMultiple logs the time it takes to execute the next middleware.
func (mw LoggingMiddleware) GetMultiple(ctx context.Context, keys ...string) (map[string]any, map[string]error) {
	defer func(begin time.Time) {
		mw.logger.Printf("method GetMultiple took: %s", time.Since(begin))
	}(time.Now())

	mw.logger.Printf("GetMultiple method invoked with keys: %s", keys)

	return mw.next.GetMultiple(ctx, keys...)
}

// List logs the time it takes to execute the next middleware.
func (mw LoggingMiddleware) List(ctx context.Context, filters ...backend.IFilter) ([]*cache.Item, error) {
	defer func(begin time.Time) {
		mw.logger.Printf("method List took: %s", time.Since(begin))
	}(time.Now())

	mw.logger.Printf("List method invoked with filters: %s", filters)

	return mw.next.List(ctx, filters...)
}

// Remove logs the time it takes to execute the next middleware.
func (mw LoggingMiddleware) Remove(ctx context.Context, keys ...string) error {
	defer func(begin time.Time) {
		mw.logger.Printf("method Remove took: %s", time.Since(begin))
	}(time.Now())

	mw.logger.Printf("Remove method invoked with keys: %s", keys)

	return mw.next.Remove(ctx, keys...)
}

// Clear logs the time it takes to execute the next middleware.
func (mw LoggingMiddleware) Clear(ctx context.Context) error {
	defer func(begin time.Time) {
		mw.logger.Printf("method Clear took: %s", time.Since(begin))
	}(time.Now())

	mw.logger.Printf("Clear method invoked")

	return mw.next.Clear(ctx)
}

// Capacity takes to execute the next middleware.
func (mw LoggingMiddleware) Capacity() int {
	return mw.next.Capacity()
}

// Allocation returns the size allocation in bytes cache.
func (mw LoggingMiddleware) Allocation() int64 {
	return mw.next.Allocation()
}

// Count takes to execute the next middleware.
func (mw LoggingMiddleware) Count(ctx context.Context) int {
	return mw.next.Count(ctx)
}

// TriggerEviction logs the time it takes to execute the next middleware.
func (mw LoggingMiddleware) TriggerEviction() {
	defer func(begin time.Time) {
		mw.logger.Printf("method TriggerEviction took: %s", time.Since(begin))
	}(time.Now())

	mw.logger.Printf("TriggerEviction method invoked")
	mw.next.TriggerEviction()
}

// Stop logs the time it takes to execute the next middleware.
func (mw LoggingMiddleware) Stop() {
	defer func(begin time.Time) {
		mw.logger.Printf("method Stop took: %s", time.Since(begin))
	}(time.Now())

	mw.logger.Printf("Stop method invoked")
	mw.next.Stop()
}

// GetStats logs the time it takes to execute the next middleware.
func (mw LoggingMiddleware) GetStats() stats.Stats {
	defer func(begin time.Time) {
		mw.logger.Printf("method GetStats took: %s", time.Since(begin))
	}(time.Now())

	mw.logger.Printf("GetStats method invoked")

	return mw.next.GetStats()
}
