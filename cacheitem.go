package hypercache

import (
	"reflect"
	"time"
)

// CacheItem is a struct that represents an item in the cache. It has a key, value, expiration duration, and a last access time field.
type CacheItem struct {
	Key         string        // key of the item
	Value       interface{}   // value of the item
	Expiration  time.Duration // expiration duration of the item
	lastAccess  time.Time     // last access time of the item
	accessCount uint          // number of times the item has been accessed
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
