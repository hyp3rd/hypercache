package serializer

import (
	"github.com/shamaton/msgpack/v2"
)

// MsgpackSerializer leverages `msgpack` to serialize the items before storing them in the cache
type MsgpackSerializer struct {
}

// Marshal serializes the given value into a byte slice.
// @param v
func (d *MsgpackSerializer) Marshal(v any) ([]byte, error) {
	return msgpack.Marshal(&v)
}

// Unmarshal deserializes the given byte slice into the given value.
// @param data
// @param v
func (d *MsgpackSerializer) Unmarshal(data []byte, v any) error {
	return msgpack.Unmarshal(data, v)
}
