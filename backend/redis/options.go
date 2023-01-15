package redis

import (
	"crypto/tls"
	"time"

	"github.com/go-redis/redis/v8"
)

type Option func(*redis.Options)

func ApplyOptions(opt *redis.Options, options ...Option) {
	for _, option := range options {
		option(opt)
	}
}

func WithAddr(addr string) Option {
	return func(opt *redis.Options) {
		opt.Addr = addr
	}
}

func WithPassword(password string) Option {
	return func(opt *redis.Options) {
		opt.Password = password
	}
}

func WithDB(db int) Option {
	return func(opt *redis.Options) {
		opt.DB = db
	}
}

func WithMaxRetries(maxRetries int) Option {
	return func(opt *redis.Options) {
		opt.MaxRetries = maxRetries
	}
}

func WithDialTimeout(dialTimeout time.Duration) Option {
	return func(opt *redis.Options) {
		opt.DialTimeout = dialTimeout
	}
}

func WithReadTimeout(readTimeout time.Duration) Option {
	return func(opt *redis.Options) {
		opt.ReadTimeout = readTimeout
	}
}

func WithWriteTimeout(writeTimeout time.Duration) Option {
	return func(opt *redis.Options) {
		opt.WriteTimeout = writeTimeout
	}
}

func WithPoolFIFO(poolFIFO bool) Option {
	return func(opt *redis.Options) {
		opt.PoolFIFO = poolFIFO
	}
}

func WithPoolSize(poolSize int) Option {
	return func(opt *redis.Options) {
		opt.PoolSize = poolSize
	}
}

func WithTLSConfig(tlsConfig *tls.Config) Option {
	return func(opt *redis.Options) {
		opt.TLSConfig = tlsConfig
	}
}
