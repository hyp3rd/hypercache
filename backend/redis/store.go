package redis

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Store is a redis store instance with redis client
type Store struct {
	Client *redis.Client
}

// New create redis store instance with given options and config
// @param opts
func New(opts ...Option) (*Store, error) {
	// Setup redis client
	opt := &redis.Options{
		Dialer: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return net.DialTimeout(network, addr, 10*time.Second)
		},
		DB:           0,
		MaxRetries:   10,
		DialTimeout:  10 * time.Second,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		PoolFIFO:     false,
		PoolSize:     20,
		MinIdleConns: 10,
		PoolTimeout:  30 * time.Second,
	}

	ApplyOptions(opt, opts...)

	if strings.TrimSpace(opt.Addr) == "" {
		return nil, fmt.Errorf("redis address is empty")
	}

	cli := redis.NewClient(opt)

	return &Store{Client: cli}, nil
}
