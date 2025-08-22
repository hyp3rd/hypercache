// Package sentinel provides standardized error definitions for the hypercache system.
// This package centralizes all error types used across the hypercache components,
// ensuring consistent error handling and messaging throughout the application.
//
// The errors defined here cover various scenarios including:
// - Invalid configuration parameters (backend types, keys, expiration, capacity)
// - Cache operation failures (key not found, expired keys, full cache)
// - Component initialization errors (nil clients, missing algorithms/serializers)
// - Runtime operation errors (timeouts, cancellations)
//
// All errors are created using the ewrap package to provide enhanced error
// wrapping and context capabilities.
package sentinel

import (
	"github.com/hyp3rd/ewrap"
)

var (
	// ErrInvalidBackendType is returned when an invalid backend type is passed to the cache.
	ErrInvalidBackendType = ewrap.New("invalid backend type")

	// ErrInvalidKey is returned when an invalid key is used to access an item in the cache.
	// An invalid key is a key that is either empty or consists only of whitespace characters.
	ErrInvalidKey = ewrap.New("invalid key")

	// ErrKeyNotFound is returned when a key is not found in the cache.
	ErrKeyNotFound = ewrap.New("key not found")

	// ErrNilValue is returned when a nil value is attempted to be set in the cache.
	ErrNilValue = ewrap.New("nil value")

	// ErrNilClient is returned when a nil client is passed to the cache.
	ErrNilClient = ewrap.New("nil client")

	// ErrKeyExpired is returned when a key is found in the cache but has expired.
	ErrKeyExpired = ewrap.New("key expired")

	// ErrInvalidExpiration is returned when an invalid expiration is passed to a cache item.
	ErrInvalidExpiration = ewrap.New("expiration cannot be negative")

	// ErrInvalidCapacity is returned when an invalid capacity is passed to the cache.
	ErrInvalidCapacity = ewrap.New("capacity cannot be negative")

	// ErrAlgorithmNotFound is returned when an algorithm is not found.
	ErrAlgorithmNotFound = ewrap.New("algorithm not found")

	// ErrStatsCollectorNotFound is returned when an algorithm is not found.
	ErrStatsCollectorNotFound = ewrap.New("stats collector not found")

	// ErrParamCannotBeEmpty is returned when a parameter cannot be empty.
	ErrParamCannotBeEmpty = ewrap.New("param cannot be empty")

	// ErrSerializerNotFound is returned when a serializer is not found.
	ErrSerializerNotFound = ewrap.New("serializer not found")

	// ErrInvalidSize is returned when an invalid size is passed to the cache.
	ErrInvalidSize = ewrap.New("invalid size")

	// ErrInvalidMaxCacheSize is returned when an invalid max cache size is passed to the cache.
	ErrInvalidMaxCacheSize = ewrap.New("invalid max cache size")

	// ErrCacheFull is returned when the cache is full.
	ErrCacheFull = ewrap.New("cache is full")

	// ErrBackendNotFound is returned when a backend is not found.
	ErrBackendNotFound = ewrap.New("backend not found")

	// ErrTimeoutOrCanceled is returned when a timeout or cancellation occurs.
	ErrTimeoutOrCanceled = ewrap.New("the operation timed out or was canceled")

	// ErrMgmtHTTPShutdownTimeout is returned when the management HTTP server fails to shutdown before context deadline.
	ErrMgmtHTTPShutdownTimeout = ewrap.New("management http shutdown timeout")
)
