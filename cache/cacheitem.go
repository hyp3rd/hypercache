package cache

// Item represents an item in the cache. It has a key, value, expiration duration, and a last access time field.

import (
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	// "https://github.com/kelindar/binary"
	"github.com/hyp3rd/hypercache/errors"
	"github.com/shamaton/msgpack/v2"
)

// Item is a struct that represents an item in the cache. It has a key, value, expiration duration, and a last access time field.
type Item struct {
	Key         string        // key of the item
	Value       any           // Value of the item
	Expiration  time.Duration // Expiration duration of the item
	LastAccess  time.Time     // LastAccess time of the item
	AccessCount uint          // AccessCount of times the item has been accessed
}

// CacheItemPool is a pool of Item values.
var CacheItemPool = sync.Pool{
	New: func() any {
		return &Item{}
	},
}

// FieldByName returns the value of the field of the Item struct with the given name.
// If the field does not exist, an empty reflect.Value is returned.
func (item *Item) FieldByName(name string) reflect.Value {
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
func (item *Item) Valid() error {
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
func (item *Item) Touch() {
	item.LastAccess = time.Now()
	item.AccessCount++
}

// Expired returns true if the item has expired, false otherwise.
func (item *Item) Expired() bool {
	// If the expiration duration is 0, the item never expires
	return item.Expiration > 0 && time.Since(item.LastAccess) > item.Expiration
}

// MarshalBinary implements the encoding.BinaryMarshaler interface.
// func (item *Item) MarshalBinary() (data []byte, err error) {
// 	buf := bytes.NewBuffer([]byte{})
// 	enc := gob.NewEncoder(buf)
// 	err = enc.Encode(item)
// 	if err != nil {
// 		return nil, err
// 	}
// 	return buf.Bytes(), nil
// }

// UnmarshalBinary implements the encoding.BinaryUnmarshaler interface.
//
//	func (item *Item) UnmarshalBinary(data []byte) error {
//		buf := bytes.NewBuffer(data)
//		dec := gob.NewDecoder(buf)
//		return dec.Decode(item)
//	}
func (item *Item) MarshalBinary() (data []byte, err error) {
	return msgpack.Marshal(item)
}

func (item *Item) UnmarshalBinary(data []byte) error {
	return msgpack.Unmarshal(data, item)
}
