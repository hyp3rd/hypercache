package rediscluster

import (
	"context"
	"net"
	"strings"

	"github.com/hyp3rd/ewrap"
	"github.com/redis/go-redis/v9"

	"github.com/hyp3rd/hypercache/internal/constants"
)

// Store is a redis cluster store instance with a ClusterClient.
type Store struct {
	Client *redis.ClusterClient
}

// New creates a redis cluster store instance with given options.
func New(opts ...Option) (*Store, error) {
	opt := &redis.ClusterOptions{
		Dialer: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialer := &net.Dialer{Timeout: constants.RedisDialTimeout}

			return dialer.DialContext(ctx, network, addr)
		},
		MaxRetries:   constants.RedisClientMaxRetries,
		ReadTimeout:  constants.RedisClientReadTimeout,
		WriteTimeout: constants.RedisClientWriteTimeout,
		PoolSize:     constants.RedisClientPoolSize,
		MinIdleConns: constants.RedisClientMinIdleConns,
		PoolTimeout:  constants.RedisClientPoolTimeout,
	}

	ApplyOptions(opt, opts...)

	if len(opt.Addrs) == 0 {
		return nil, ewrap.New("redis cluster addrs are empty")
	}

	for _, addr := range opt.Addrs {
		if strings.TrimSpace(addr) == "" {
			return nil, ewrap.New("redis cluster address is empty")
		}
	}

	cli := redis.NewClusterClient(opt)

	return &Store{Client: cli}, nil
}
