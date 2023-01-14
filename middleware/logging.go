package middleware

import (
	"time"

	"github.com/hyp3rd/hypercache"
)

// Logger describes a logging interface allowing to implement different external, or custom logger.
// Tested with logrus, and Uber's Zap (high-performance), but should work with any other logger that matches the interface.
type Logger interface {
	Infof(format string, v ...interface{})
	Errorf(format string, v ...interface{})
}

// LoggingMiddleware is a middleware that logs the time it takes to execute the next middleware.
// Must implement the hypercache.HyperCacheService interface.
type LoggingMiddleware struct {
	next   hypercache.HyperCacheService
	logger Logger
}

// NewLoggingMiddleware returns a new LoggingMiddleware.
func NewLoggingMiddleware(next hypercache.HyperCacheService, logger Logger) hypercache.HyperCacheService {
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
