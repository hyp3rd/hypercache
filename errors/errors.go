package errors

import "errors"

var (
	// ErrInvalidBackendType is returned when an invalid backend type is passed to the cache.
	ErrInvalidBackendType = errors.New("invalid backend type")

	// ErrInvalidKey is returned when an invalid key is used to access an item in the cache.
	// An invalid key is a key that is either empty or consists only of whitespace characters.
	ErrInvalidKey = errors.New("invalid key")

	// ErrKeyNotFound is returned when a key is not found in the cache.
	ErrKeyNotFound = errors.New("key not found")

	// ErrNilValue is returned when a nil value is attempted to be set in the cache.
	ErrNilValue = errors.New("nil value")

	// ErrNilClient is returned when a nil client is passed to the cache.
	ErrNilClient = errors.New("nil client")

	// ErrKeyExpired is returned when a key is found in the cache but has expired.
	ErrKeyExpired = errors.New("key expired")

	// ErrInvalidExpiration is returned when an invalid expiration is passed to a cache item.
	ErrInvalidExpiration = errors.New("expiration cannot be negative")

	// ErrInvalidCapacity is returned when an invalid capacity is passed to the cache.
	ErrInvalidCapacity = errors.New("capacity cannot be negative")

	// ErrAlgorithmNotFound is returned when an algorithm is not found.
	ErrAlgorithmNotFound = errors.New("algorithm not found")

	// ErrStatsCollectorNotFound is returned when an algorithm is not found.
	ErrStatsCollectorNotFound = errors.New("stats collector not found")

	// ErrParamCannotBeEmpty is returned when a parameter cannot be empty.
	ErrParamCannotBeEmpty = errors.New("param cannot be empty")

	// ErrSerializerNotFound is returned when a serializer is not found.
	ErrSerializerNotFound = errors.New("serializer not found")

	// ErrInvalidSize is returned when an invalid size is passed to the cache.
	ErrInvalidSize = errors.New("invalid size")

	// ErrInvalidMaxCacheSize is returned when an invalid max cache size is passed to the cache.
	ErrInvalidMaxCacheSize = errors.New("invalid max cache size")

	// ErrCacheFull is returned when the cache is full.
	ErrCacheFull = errors.New("cache is full")
)
