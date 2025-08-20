package types

// Item represents an item in the cache. It has a key, value, expiration duration, and a last access time field.

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ugorji/go/codec"

	"github.com/hyp3rd/hypercache/sentinel"
)

const (
	bytesPerKB = 1024
)

// ItemPoolManager manages Item object pools for memory efficiency.
type ItemPoolManager struct {
	pool sync.Pool
}

// NewItemPoolManager creates a new ItemPoolManager with default configuration.
func NewItemPoolManager() *ItemPoolManager {
	return &ItemPoolManager{
		pool: sync.Pool{
			New: func() any {
				return &Item{}
			},
		},
	}
}

// Get retrieves an Item from the pool.
func (m *ItemPoolManager) Get() *Item {
	item, ok := m.pool.Get().(*Item)
	if !ok {
		return &Item{}
	}

	return item
}

// Put returns an Item to the pool.
func (m *ItemPoolManager) Put(item *Item) {
	m.pool.Put(item)
}

// Item is a struct that represents an item in the cache. It has a key, value, expiration duration, and a last access time field.
type Item struct {
	Key         string        // key of the item
	Value       any           // Value of the item
	Size        int64         // Size of the item, in bytes
	Expiration  time.Duration // Expiration duration of the item
	LastAccess  time.Time     // LastAccess time of the item
	AccessCount uint          // AccessCount of times the item has been accessed
}

// SetSize stores the size of the Item in bytes.
func (item *Item) SetSize() error {
	// Use local buffer for thread safety
	var buf []byte

	enc := codec.NewEncoderBytes(&buf, &codec.CborHandle{})

	err := enc.Encode(item.Value)
	if err != nil {
		return sentinel.ErrInvalidSize
	}

	item.Size = int64(len(buf))

	return nil
}

// SizeMB returns the size of the Item in megabytes.
func (item *Item) SizeMB() float64 {
	return float64(item.Size) / (bytesPerKB * bytesPerKB)
}

// SizeKB returns the size of the Item in kilobytes.
func (item *Item) SizeKB() float64 {
	return float64(item.Size) / bytesPerKB
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
		return sentinel.ErrInvalidKey
	}

	// Check for nil value
	if item.Value == nil {
		return sentinel.ErrNilValue
	}

	// Check for negative expiration
	if atomic.LoadInt64((*int64)(&item.Expiration)) < 0 {
		atomic.StoreInt64((*int64)(&item.Expiration), 0)

		return sentinel.ErrInvalidExpiration
	}

	return nil
}

// Expired returns true if the item has expired, false otherwise.
func (item *Item) Expired() bool {
	// If the expiration duration is 0, the item never expires
	return item.Expiration > 0 && time.Since(item.LastAccess) > item.Expiration
}
