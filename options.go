package hypercache

import "time"

// Option is a function that configures an HyperCache instance.
type Option func(*HyperCache)

// WithExpirationInterval is an Option for the NewHyperCache function that sets the expiration interval for the cache.
// If the given expiration interval is negative, it defaults to 1 minute.
func WithExpirationInterval(expirationInterval time.Duration) Option {
	return func(c *HyperCache) {
		if expirationInterval < 0 {
			expirationInterval = time.Minute
		}
		c.expirationInterval = expirationInterval
	}
}

// WithEvictionInterval is an Option for the NewHyperCache function that sets the eviction interval for the cache.
// If the given eviction interval is negative, it defaults to 0, which means that the eviction loop is disabled.
func WithEvictionInterval(evictionInterval time.Duration) Option {
	return func(c *HyperCache) {
		if evictionInterval < 0 {
			evictionInterval = 0
		}
		c.evictionInterval = evictionInterval
	}
}
