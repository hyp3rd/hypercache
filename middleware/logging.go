package middleware

import (
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/models"
	"github.com/hyp3rd/hypercache/stats"
)

// Logger describes a logging interface allowing to implement different external, or custom logger.
// Tested with logrus, and Uber's Zap (high-performance), but should work with any other logger that matches the interface.
type Logger interface {
	Printf(format string, v ...interface{})
	// Errorf(format string, v ...interface{})
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
func (mw LoggingMiddleware) Get(key string) (value interface{}, ok bool) {
	defer func(begin time.Time) {
		mw.logger.Printf("method Get took: %s", time.Since(begin))
	}(time.Now())

	mw.logger.Printf("Get method called with key: %s", key)
	return mw.next.Get(key)
}

// Set logs the time it takes to execute the next middleware.
func (mw LoggingMiddleware) Set(key string, value any, expiration time.Duration) error {
	defer func(begin time.Time) {
		mw.logger.Printf("method Set took: %s", time.Since(begin))
	}(time.Now())

	mw.logger.Printf("Set method called with key: %s value: %s", key, value)
	return mw.next.Set(key, value, expiration)
}

// GetOrSet logs the time it takes to execute the next middleware.
func (mw LoggingMiddleware) GetOrSet(key string, value any, expiration time.Duration) (any, error) {
	defer func(begin time.Time) {
		mw.logger.Printf("method GetOrSet took: %s", time.Since(begin))
	}(time.Now())

	mw.logger.Printf("GetOrSet method invoked with key: %s value: %s", key, value)
	return mw.next.GetOrSet(key, value, expiration)
}

// GetWithInfo logs the time it takes to execute the next middleware.
func (mw LoggingMiddleware) GetWithInfo(key string) (item *models.Item, ok bool) {
	defer func(begin time.Time) {
		mw.logger.Printf("method GetWithInfo took: %s", time.Since(begin))
	}(time.Now())

	mw.logger.Printf("GetWithInfo method invoked with key: %s", key)
	return mw.next.GetWithInfo(key)
}

// GetMultiple logs the time it takes to execute the next middleware.
func (mw LoggingMiddleware) GetMultiple(keys ...string) (result map[string]any, failed map[string]error) {
	defer func(begin time.Time) {
		mw.logger.Printf("method GetMultiple took: %s", time.Since(begin))
	}(time.Now())

	mw.logger.Printf("GetMultiple method invoked with keys: %s", keys)
	return mw.next.GetMultiple(keys...)
}

// List logs the time it takes to execute the next middleware.
func (mw LoggingMiddleware) List(filters ...any) ([]*models.Item, error) {
	defer func(begin time.Time) {
		mw.logger.Printf("method List took: %s", time.Since(begin))
	}(time.Now())

	mw.logger.Printf("List method invoked with filters: %s", filters)
	return mw.next.List(filters...)
}

// Remove logs the time it takes to execute the next middleware.
func (mw LoggingMiddleware) Remove(keys ...string) {
	defer func(begin time.Time) {
		mw.logger.Printf("method Remove took: %s", time.Since(begin))
	}(time.Now())

	mw.logger.Printf("Remove method invoked with keys: %s", keys)
	mw.next.Remove(keys...)
}

// Clear logs the time it takes to execute the next middleware.
func (mw LoggingMiddleware) Clear() error {
	defer func(begin time.Time) {
		mw.logger.Printf("method Clear took: %s", time.Since(begin))
	}(time.Now())

	mw.logger.Printf("Clear method invoked")
	return mw.next.Clear()
}

// Capacity takes to execute the next middleware.
func (mw LoggingMiddleware) Capacity() int {
	return mw.next.Capacity()
}

// Allocation returns the size allocation in bytes cache
func (mw LoggingMiddleware) Allocation() int64 {
	return mw.next.Allocation()
}

// Count takes to execute the next middleware.
func (mw LoggingMiddleware) Count() int {
	return mw.next.Count()
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
