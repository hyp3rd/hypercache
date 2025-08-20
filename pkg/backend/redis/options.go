// Package redis provides configuration options and utilities for Redis backend implementation.
// It includes functional options for configuring redis.Options and helper functions
// to apply these configurations in a flexible and composable manner.
package redis

import (
	"crypto/tls"
	"time"

	"github.com/redis/go-redis/v9"
)

// Option is a function type that can be used to configure the `Redis`.
type Option func(*redis.Options)

// ApplyOptions applies the given options to the given backend.
func ApplyOptions(opt *redis.Options, options ...Option) {
	for _, option := range options {
		option(opt)
	}
}

// WithAddr sets the `Addr` field of the `redis.Options` struct.
func WithAddr(addr string) Option {
	return func(opt *redis.Options) {
		opt.Addr = addr
	}
}

// WithUsername sets the `Username` field of the `redis.Options` struct.
func WithUsername(username string) Option {
	return func(opt *redis.Options) {
		opt.Username = username
	}
}

// WithPassword sets the `Password` field of the `redis.Options` struct.
func WithPassword(password string) Option {
	return func(opt *redis.Options) {
		opt.Password = password
	}
}

// WithDB sets the `DB` field of the `redis.Options` struct.
func WithDB(db int) Option {
	return func(opt *redis.Options) {
		opt.DB = db
	}
}

// WithMaxRetries sets the `MaxRetries` field of the `redis.Options` struct.
func WithMaxRetries(maxRetries int) Option {
	return func(opt *redis.Options) {
		opt.MaxRetries = maxRetries
	}
}

// WithDialTimeout sets the `DialTimeout` field of the `redis.Options` struct.
func WithDialTimeout(dialTimeout time.Duration) Option {
	return func(opt *redis.Options) {
		opt.DialTimeout = dialTimeout
	}
}

// WithReadTimeout sets the `ReadTimeout` field of the `redis.Options` struct.
func WithReadTimeout(readTimeout time.Duration) Option {
	return func(opt *redis.Options) {
		opt.ReadTimeout = readTimeout
	}
}

// WithWriteTimeout sets the `WriteTimeout` field of the `redis.Options` struct.
func WithWriteTimeout(writeTimeout time.Duration) Option {
	return func(opt *redis.Options) {
		opt.WriteTimeout = writeTimeout
	}
}

// WithPoolFIFO sets the `PoolFIFO` field of the `redis.Options` struct.
func WithPoolFIFO(poolFIFO bool) Option {
	return func(opt *redis.Options) {
		opt.PoolFIFO = poolFIFO
	}
}

// WithPoolSize sets the `PoolSize` field of the `redis.Options` struct.
func WithPoolSize(poolSize int) Option {
	return func(opt *redis.Options) {
		opt.PoolSize = poolSize
	}
}

// WithPoolTimeout sets the `PoolTimeout` field of the `redis.Options` struct.
func WithPoolTimeout(poolTimeout time.Duration) Option {
	return func(opt *redis.Options) {
		opt.PoolTimeout = poolTimeout
	}
}

// WithTLSConfig sets the `TLSConfig` field of the `redis.Options` struct.
func WithTLSConfig(tlsConfig *tls.Config) Option {
	return func(opt *redis.Options) {
		opt.TLSConfig = tlsConfig
	}
}

// WithMinIdleConns sets the `MinIdleConns` field of the `redis.Options` struct.
func WithMinIdleConns(minIdleConns int) Option {
	return func(opt *redis.Options) {
		opt.MinIdleConns = minIdleConns
	}
}
