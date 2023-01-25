package models

// Item represents an item in the cache. It has a key, value, expiration duration, and a last access time field.

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hyp3rd/hypercache/errors"
	"github.com/ugorji/go/codec"
)

var (
	// ItemPool is a pool of Item values.
	ItemPool = sync.Pool{
		New: func() any {
			return &Item{}
		},
	}

	// buf is a buffer used to calculate the size of the item.
	// buf bytes.Buffer

	// encoderPool is a pool of encoders used to calculate the size of the item.
	// encoderPool = sync.Pool{
	// 	New: func() any {
	// 		return gob.NewEncoder(&buf)
	// 	},
	// }

	buf []byte

	// encoderPool is a pool of encoders used to calculate the size of the item.
	encoderPool = sync.Pool{
		New: func() any {
			return codec.NewEncoderBytes(&buf, &codec.CborHandle{})
		},
	}
)

// Item is a struct that represents an item in the cache. It has a key, value, expiration duration, and a last access time field.
type Item struct {
	Key         string        // key of the item
	Value       any           // Value of the item
	Size        int64         // Size of the item, in bytes
	Expiration  time.Duration // Expiration duration of the item
	LastAccess  time.Time     // LastAccess time of the item
	AccessCount uint          // AccessCount of times the item has been accessed
}

// Size returns the size of the Item in bytes
// func (i *Item) SetSize() error {
// 	// Get an encoder from the pool
// 	enc := encoderPool.Get().(*gob.Encoder)
// 	// Reset the buffer and put the encoder back in the pool
// 	defer buf.Reset()
// 	defer encoderPool.Put(enc)

// 	// Encode the item
// 	if err := enc.Encode(i.Value); err != nil {
// 		return errors.ErrInvalidSize
// 	}
// 	// Set the size of the item
// 	i.Size = int64(buf.Len())
// 	return nil
// }

func (i *Item) SetSize() error {
	enc := encoderPool.Get().(*codec.Encoder)
	defer encoderPool.Put(enc)
	if err := enc.Encode(i.Value); err != nil {
		return errors.ErrInvalidSize
	}

	i.Size = int64(len(buf))
	buf = buf[:0]
	return nil
}

// SizeMB returns the size of the Item in megabytes
func (i *Item) SizeMB() float64 {
	return float64(i.Size) / (1024 * 1024)
}

// SizeKB returns the size of the Item in kilobytes
func (i *Item) SizeKB() float64 {
	return float64(i.Size) / 1024
}

// Touch updates the last access time of the item and increments the access count.
func (item *Item) Touch() {
	item.LastAccess = time.Now()
	item.AccessCount++
}

// Valid returns an error if the item is invalid, nil otherwise.
func (item *Item) Valid() error {
	// Check for empty key
	if strings.TrimSpace(item.Key) == "" {
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

// Expired returns true if the item has expired, false otherwise.
func (item *Item) Expired() bool {
	// If the expiration duration is 0, the item never expires
	return item.Expiration > 0 && time.Since(item.LastAccess) > item.Expiration
}
