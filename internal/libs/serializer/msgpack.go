package serializer

import (
	"github.com/hyp3rd/ewrap"
)

// MsgpackSerializer leverages `msgpack` to serialize the items before storing them in the cache.
//
// Deprecated: This serializer is now a shim and will be removed in a future release for security reasons.
// REF: https://github.com/shamaton/msgpack/pull/60
// Please use the `Marshal` method of the `Serializer` interface instead.
type MsgpackSerializer struct{}

// Marshal serializes the given value into a byte slice.
// @param v.
//
// Deprecated: This method is now a shim and will be removed in a future release for security reasons.
// REF: https://github.com/shamaton/msgpack/pull/60
// Please use the `Marshal` method of the `Serializer` interface instead.
func (*MsgpackSerializer) Marshal(_ any) ([]byte, error) { // receiver omitted (unused)
	// data, err := msgpack.Marshal(&v)
	// if err != nil {
	// 	return nil, ewrap.Wrap(err, "failed to marshal msgpack")
	// }

	// return data, nil
	return nil, ewrap.New("msgpack serialization is deprecated and has been disabled for security reasons")
}

// Unmarshal deserializes the given byte slice into the given value.
// @param data
// @param v.
//
// Deprecated: This method is now a shim and will be removed in a future release for security reasons.
// REF: https://github.com/shamaton/msgpack/pull/60
// Please use the `Unmarshal` method of the `Serializer` interface instead.
func (*MsgpackSerializer) Unmarshal(_ []byte, _ any) error { // receiver omitted (unused)
	// err := msgpack.Unmarshal(data, v)
	// if err != nil {
	// 	return ewrap.Wrap(err, "failed to unmarshal msgpack")
	// }

	// return nil
	return ewrap.New("msgpack deserialization is deprecated and has been disabled for security reasons")
}
