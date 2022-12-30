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

// WithExpirationDisabled disables the expiration loop.
func WithExpirationDisabled(c *HyperCache) {
	c.expirationInterval = 0
}

// WithEvictionDisabled disables the eviction loop.
func WithEvictionDisabled(c *HyperCache) {
	c.evictionInterval = 0
}

// WithMaxEvictionCount sets the maximum number of items that can be evicted in a single eviction loop iteration.
// func WithMaxEvictionCount(count int) Option {
// 	return func(c *HyperCache) {
// 		c.maxEvictionCount = count
// 	}
// }

// CacheItemOption is a function that sets options for a CacheItem.
type CacheItemOption func(*CacheItem)

// WithDuration sets the expiration duration for a cache item.
func WithDuration(duration time.Duration) CacheItemOption {
	return func(item *CacheItem) {
		item.Duration = duration
	}
}

// OnItemExpired sets the OnItemExpired callback function for a cache item.
func OnItemExpired(onExpired func(key string, value interface{})) CacheItemOption {
	return func(item *CacheItem) {
		item.OnItemExpired = onExpired
	}
}
