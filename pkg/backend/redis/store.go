package redis

import (
	"context"
	"net"
	"strings"

	"github.com/hyp3rd/ewrap"
	"github.com/redis/go-redis/v9"

	"github.com/hyp3rd/hypercache/internal/constants"
)

// Store is a redis store instance with redis client.
type Store struct {
	Client *redis.Client
}

// New create redis store instance with given options and config
// @param opts.
func New(opts ...Option) (*Store, error) {
	// Setup redis client
	opt := &redis.Options{
		Dialer: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialer := &net.Dialer{
				Timeout: constants.RedisDialTimeout,
			}

			return dialer.DialContext(ctx, network, addr)
		},
		DB:           0,
		MaxRetries:   constants.RedisClientMaxRetries,
		DialTimeout:  constants.RedisDialTimeout,
		ReadTimeout:  constants.RedisClientReadTimeout,
		WriteTimeout: constants.RedisClientWriteTimeout,
		PoolFIFO:     false,
		PoolSize:     constants.RedisClientPoolSize,
		MinIdleConns: constants.RedisClientMinIdleConns,
		PoolTimeout:  constants.RedisClientPoolTimeout,
	}

	ApplyOptions(opt, opts...)

	if strings.TrimSpace(opt.Addr) == "" {
		return nil, ewrap.New("redis address is empty")
	}

	cli := redis.NewClient(opt)

	return &Store{Client: cli}, nil
}
