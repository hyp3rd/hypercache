package serializer

import (
	"github.com/shamaton/msgpack/v2"

	"github.com/hyp3rd/ewrap"
)

// MsgpackSerializer leverages `msgpack` to serialize the items before storing them in the cache.
type MsgpackSerializer struct{}

// Marshal serializes the given value into a byte slice.
// @param v.
func (*MsgpackSerializer) Marshal(v any) ([]byte, error) { // receiver omitted (unused)
	data, err := msgpack.Marshal(&v)
	if err != nil {
		return nil, ewrap.Wrap(err, "failed to marshal msgpack")
	}

	return data, nil
}

// Unmarshal deserializes the given byte slice into the given value.
// @param data
// @param v.
func (*MsgpackSerializer) Unmarshal(data []byte, v any) error { // receiver omitted (unused)
	err := msgpack.Unmarshal(data, v)
	if err != nil {
		return ewrap.Wrap(err, "failed to unmarshal msgpack")
	}

	return nil
}
