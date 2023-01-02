package hypercache

import (
	"reflect"
	"time"
)

// CacheItem is a struct that represents an item in the cache. It has a key, value, expiration duration, and a last access time field.
type CacheItem struct {
	Value      interface{}   // value of the item
	Expiration time.Duration // expiration duration of the item
	// Expiration  int64     // monotonic clock value in nanoseconds
	lastAccess  time.Time // last access time of the item
	accessCount uint      // number of times the item has been accessed
}

// FieldByName returns the value of the field of the CacheItem struct with the given name.
// If the field does not exist, an empty reflect.Value is returned.
func (item *CacheItem) FieldByName(name string) reflect.Value {
	// Get the reflect.Value of the item pointer
	v := reflect.ValueOf(item)

	// Get the reflect.Value of the item struct itself by calling Elem() on the pointer value
	f := v.Elem().FieldByName(name)

	// If the field does not exist, return an empty reflect.Value
	if !f.IsValid() {
		return reflect.Value{}
	}
	// Return the field value
	return f
}

// Valid returns an error if the item is invalid, nil otherwise.
func (item *CacheItem) Valid() error {
	// Check for nil value
	if item.Value == nil {
		return ErrNilValue
	}

	// Check for negative expiration
	if item.Expiration < 0 {
		return ErrInvalidExpiration
	}

	// Check for negative expiration
	// if atomic.LoadInt64((*int64)(&item.Expiration)) < 0 {
	// 	// atomic.StoreInt64((*int64)(&item.Expiration), 0)
	// 	return ErrInvalidExpiration
	// }

	return nil
}

// Touch updates the last access time of the item and increments the access count.
func (item *CacheItem) Touch() {
	item.lastAccess = time.Now()
	item.accessCount++
}

// Expired returns true if the item has expired, false otherwise.
func (item *CacheItem) Expired() bool {
	// If the expiration duration is 0, the item never expires
	return item.Expiration > 0 && time.Since(item.lastAccess) > item.Expiration
	// return item.Expiration < time.Now().UnixNano()
}
