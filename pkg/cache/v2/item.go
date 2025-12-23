package v2

import (
	"bytes"
	"encoding"
	"strings"
	"sync"
	"time"

	"github.com/ugorji/go/codec"

	"github.com/hyp3rd/hypercache/internal/sentinel"
)

const bytesPerKB = 1024

// Global pools and codec handle for zero-alloc SetSize.
// These are intentionally package-scoped to amortize allocations across calls.
//
//nolint:gochecknoglobals
var cborHandle = &codec.CborHandle{}

//nolint:gochecknoglobals
var bufPool = sync.Pool{ // *bytes.Buffer
	New: func() any { return new(bytes.Buffer) },
}

// ItemPoolManager manages Item object pools for memory efficiency (v2).
type ItemPoolManager struct {
	pool sync.Pool
}

// NewItemPoolManager creates a new ItemPoolManager with default configuration (v2).
func NewItemPoolManager() *ItemPoolManager {
	return &ItemPoolManager{
		pool: sync.Pool{New: func() any { return &Item{} }},
	}
}

// Get retrieves an Item from the pool (v2).
func (m *ItemPoolManager) Get() *Item {
	if v, ok := m.pool.Get().(*Item); ok {
		return v
	}

	return &Item{}
}

// Put returns an Item to the pool (v2).
func (m *ItemPoolManager) Put(it *Item) {
	if it == nil {
		return
	}
	// Zero to avoid retaining references across pool reuses
	*it = Item{}
	m.pool.Put(it)
}

// Item is a struct that represents an item in the cache (v2 optimized layout).
// It mirrors pkg/cache.Item behavior but with minor layout tweaks for locality.
type Item struct {
	Key         string        // key of the item
	Value       any           // value of the item
	LastAccess  time.Time     // last access time
	LastUpdated time.Time     // last write/version assignment time (distributed usage)
	Size        int64         // size in bytes
	Expiration  time.Duration // expiration duration
	AccessCount uint32        // number of times the item has been accessed
	Version     uint64        // logical version (monotonic per key)
	Origin      string        // originating node id (tiebreaker)
}

// Touch updates last access time and increments access count.
func (it *Item) Touch() {
	it.LastAccess = time.Now()
	it.AccessCount++
}

// SizeMB returns the size of the Item in megabytes.
func (it *Item) SizeMB() float64 { return float64(it.Size) / (bytesPerKB * bytesPerKB) }

// SizeKB returns the size of the Item in kilobytes.
func (it *Item) SizeKB() float64 { return float64(it.Size) / bytesPerKB }

// Valid returns an error if the item is invalid, nil otherwise.
// Semantics match pkg/cache.Item.Valid but without atomics.
func (it *Item) Valid() error {
	if strings.TrimSpace(it.Key) == "" {
		return sentinel.ErrInvalidKey
	}

	if it.Value == nil {
		return sentinel.ErrNilValue
	}

	if it.Expiration < 0 {
		it.Expiration = 0

		return sentinel.ErrInvalidExpiration
	}

	return nil
}

// Expired reports whether the item has expired.
func (it *Item) Expired() bool {
	return it.Expiration > 0 && time.Since(it.LastAccess) > it.Expiration
}

// Sizer allows custom values to report their encoded size without serialization.
type Sizer interface{ SizeBytes() int }

// SetSize computes and sets Size using fast paths and pooled encoder/buffer.
// This preserves original behavior (size of serialized value) but reduces allocations.
func (it *Item) SetSize() error {
	// Fast paths for common types
	switch val := it.Value.(type) {
	case []byte:
		it.Size = int64(len(val))

		return nil

	case string:
		it.Size = int64(len(val))

		return nil

	case encoding.BinaryMarshaler:
		b, err := val.MarshalBinary()
		if err != nil {
			return sentinel.ErrInvalidSize
		}

		it.Size = int64(len(b))

		return nil

	case Sizer:
		it.Size = int64(val.SizeBytes())

		return nil
	}

	// Fallback to CBOR encoding into a pooled bytes.Buffer
	bufAny := bufPool.Get()

	buf, ok := bufAny.(*bytes.Buffer)
	if !ok {
		// extremely unlikely; create a new buffer
		buf = new(bytes.Buffer)
	}

	buf.Reset()

	// Avoid retaining huge buffers in the pool
	const maxKeepCap = 1 << 20 // 1 MiB

	defer func() {
		if buf.Cap() > maxKeepCap {
			// let GC reclaim a too-large buffer
			return
		}

		buf.Reset()
		bufPool.Put(buf)
	}()

	enc := codec.NewEncoder(buf, cborHandle)

	err := enc.Encode(it.Value)
	if err != nil {
		return sentinel.ErrInvalidSize
	}

	it.Size = int64(buf.Len())

	return nil
}
