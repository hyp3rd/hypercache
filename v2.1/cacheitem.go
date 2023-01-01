package hypercacheV2

import "time"

// CacheItem represents an item in the cache. It stores the key, value, last accessed time, and duration.
type CacheItem struct {
	Key                string                              // Key is the key of the item
	Value              interface{}                         // Value is the value stored in the cache
	LastAccessedBefore time.Time                           // LastAccessedBefore is the time before which the item was last accessed
	Duration           time.Duration                       // Duration is the time after which the item expires
	OnItemExpired      func(key string, value interface{}) // callback function to be executed when the item expires
	OnItemEvicted      func(key string, value interface{}) // callback function to be executed when the item is evicted
}

func (item *CacheItem) Touch() {
	item.LastAccessedBefore = time.Now()
}
