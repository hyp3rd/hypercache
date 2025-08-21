// Package rediscluster provides configuration options and utilities for Redis Cluster backend implementation.
package rediscluster

import (
	"crypto/tls"
	"time"

	"github.com/redis/go-redis/v9"
)

// Option is a function type that can be used to configure the redis ClusterClient Options.
type Option func(*redis.ClusterOptions)

// ApplyOptions applies a list of options to the provided ClusterOptions.
func ApplyOptions(opt *redis.ClusterOptions, options ...Option) {
	for _, option := range options {
		option(opt)
	}
}

// WithAddrs sets the Addrs for cluster nodes.
func WithAddrs(addrs ...string) Option {
	return func(opt *redis.ClusterOptions) {
		opt.Addrs = addrs
	}
}

// WithUsername sets Username.
func WithUsername(username string) Option {
	return func(opt *redis.ClusterOptions) {
		opt.Username = username
	}
}

// WithPassword sets Password.
func WithPassword(password string) Option {
	return func(opt *redis.ClusterOptions) {
		opt.Password = password
	}
}

// WithMaxRetries sets MaxRetries.
func WithMaxRetries(maxRetries int) Option {
	return func(opt *redis.ClusterOptions) {
		opt.MaxRetries = maxRetries
	}
}

// WithReadTimeout sets ReadTimeout.
func WithReadTimeout(readTimeout time.Duration) Option {
	return func(opt *redis.ClusterOptions) {
		opt.ReadTimeout = readTimeout
	}
}

// WithWriteTimeout sets WriteTimeout.
func WithWriteTimeout(writeTimeout time.Duration) Option {
	return func(opt *redis.ClusterOptions) {
		opt.WriteTimeout = writeTimeout
	}
}

// WithTLSConfig sets TLSConfig.
func WithTLSConfig(tlsConfig *tls.Config) Option {
	return func(opt *redis.ClusterOptions) {
		opt.TLSConfig = tlsConfig
	}
}

// WithPoolSize sets PoolSize.
func WithPoolSize(size int) Option {
	return func(opt *redis.ClusterOptions) {
		opt.PoolSize = size
	}
}

// WithMinIdleConns sets MinIdleConns.
func WithMinIdleConns(minIdle int) Option {
	return func(opt *redis.ClusterOptions) {
		opt.MinIdleConns = minIdle
	}
}

// WithPoolTimeout sets PoolTimeout.
func WithPoolTimeout(d time.Duration) Option {
	return func(opt *redis.ClusterOptions) {
		opt.PoolTimeout = d
	}
}
