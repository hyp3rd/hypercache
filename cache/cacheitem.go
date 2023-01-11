package cache

// CacheItem represents an item in the cache. It has a key, value, expiration duration, and a last access time field.

import (
	"bytes"
	"encoding/gob"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hyp3rd/hypercache/errors"
)

// CacheItem is a struct that represents an item in the cache. It has a key, value, expiration duration, and a last access time field.
type CacheItem struct {
	Key         string        // key of the item
	Value       interface{}   // value of the item
	Expiration  time.Duration // expiration duration of the item
	LastAccess  time.Time     // last access time of the item
	AccessCount uint          // number of times the item has been accessed
}

// CacheItemPool is a pool of CacheItem values.
var CacheItemPool = sync.Pool{
	New: func() interface{} {
		return &CacheItem{}
	},
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
	// Check for empty key
	if item.Key == "" || strings.TrimSpace(item.Key) == "" {
		return errors.ErrInvalidKey
	}

	// Check for nil value
	if item.Value == nil {
		return errors.ErrNilValue
	}

	// Check for negative expiration
	if atomic.LoadInt64((*int64)(&item.Expiration)) < 0 {
		atomic.StoreInt64((*int64)(&item.Expiration), 0)
		return errors.ErrInvalidExpiration
	}
	return nil
}

// Touch updates the last access time of the item and increments the access count.
func (item *CacheItem) Touch() {
	item.LastAccess = time.Now()
	item.AccessCount++
}

// Expired returns true if the item has expired, false otherwise.
func (item *CacheItem) Expired() bool {
	// If the expiration duration is 0, the item never expires
	return item.Expiration > 0 && time.Since(item.LastAccess) > item.Expiration
}

// MarshalBinary implements the encoding.BinaryMarshaler interface.
func (item *CacheItem) MarshalBinary() (data []byte, err error) {
	buf := bytes.NewBuffer([]byte{})
	enc := gob.NewEncoder(buf)
	err = enc.Encode(item)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// UnmarshalBinary implements the encoding.BinaryUnmarshaler interface.
func (item *CacheItem) UnmarshalBinary(data []byte) error {
	buf := bytes.NewBuffer(data)
	dec := gob.NewDecoder(buf)
	return dec.Decode(item)
}
