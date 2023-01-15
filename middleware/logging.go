package middleware

import (
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/cache"
	"github.com/hyp3rd/hypercache/stats"
)

// Logger describes a logging interface allowing to implement different external, or custom logger.
// Tested with logrus, and Uber's Zap (high-performance), but should work with any other logger that matches the interface.
type Logger interface {
	Infof(format string, v ...interface{})
	Errorf(format string, v ...interface{})
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
		mw.logger.Infof("method Get took: %s", time.Since(begin))
	}(time.Now())

	mw.logger.Infof("Get method called with key: %s", key)
	return mw.next.Get(key)
}

// Set logs the time it takes to execute the next middleware.
func (mw LoggingMiddleware) Set(key string, value any, expiration time.Duration) error {
	defer func(begin time.Time) {
		mw.logger.Infof("method Set took: %s", time.Since(begin))
	}(time.Now())

	mw.logger.Infof("Set method called with key: %s value: %s", key, value)
	return mw.next.Set(key, value, expiration)
}

// GetOrSet logs the time it takes to execute the next middleware.
func (mw LoggingMiddleware) GetOrSet(key string, value any, expiration time.Duration) (any, error) {
	defer func(begin time.Time) {
		mw.logger.Infof("method GetOrSet took: %s", time.Since(begin))
	}(time.Now())

	mw.logger.Infof("GetOrSet method invoked with key: %s value: %s", key, value)
	return mw.next.GetOrSet(key, value, expiration)
}

// GetMultiple logs the time it takes to execute the next middleware.
func (mw LoggingMiddleware) GetMultiple(keys ...string) (result map[string]any, failed map[string]error) {
	defer func(begin time.Time) {
		mw.logger.Infof("method GetMultiple took: %s", time.Since(begin))
	}(time.Now())

	mw.logger.Infof("GetMultiple method invoked with keys: %s", keys)
	return mw.next.GetMultiple(keys...)
}

// List
func (mw LoggingMiddleware) List(filters ...any) ([]*cache.CacheItem, error) {
	defer func(begin time.Time) {
		mw.logger.Infof("method List took: %s", time.Since(begin))
	}(time.Now())

	mw.logger.Infof("List method invoked with filters: %s", filters)
	return mw.next.List(filters...)
}

// Remove
func (mw LoggingMiddleware) Remove(keys ...string) {
	defer func(begin time.Time) {
		mw.logger.Infof("method Remove took: %s", time.Since(begin))
	}(time.Now())

	mw.logger.Infof("Remove method invoked with keys: %s", keys)
	mw.next.Remove(keys...)
}

// Clear
func (mw LoggingMiddleware) Clear() error {
	defer func(begin time.Time) {
		mw.logger.Infof("method Clear took: %s", time.Since(begin))
	}(time.Now())

	mw.logger.Infof("Clear method invoked")
	return mw.next.Clear()
}

// Capacity
func (mw LoggingMiddleware) Capacity() int {
	return mw.next.Capacity()
}

// Size
func (mw LoggingMiddleware) Size() int {
	return mw.next.Size()
}

func (mw LoggingMiddleware) TriggerEviction() {
	defer func(begin time.Time) {
		mw.logger.Infof("method TriggerEviction took: %s", time.Since(begin))
	}(time.Now())

	mw.logger.Infof("TriggerEviction method invoked")
	mw.next.TriggerEviction()
}

// Stop
func (mw LoggingMiddleware) Stop() {
	defer func(begin time.Time) {
		mw.logger.Infof("method Stop took: %s", time.Since(begin))
	}(time.Now())

	mw.logger.Infof("Stop method invoked")
	mw.next.Stop()
}

// GetStats
func (mw LoggingMiddleware) GetStats() stats.Stats {
	defer func(begin time.Time) {
		mw.logger.Infof("method GetStats took: %s", time.Since(begin))
	}(time.Now())

	mw.logger.Infof("GetStats method invoked")
	return mw.next.GetStats()
}