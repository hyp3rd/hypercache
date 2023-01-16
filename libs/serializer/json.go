package serializer

import "encoding/json"

// DefaultJSONSerializer leverages the default `json` to serialize the items before storing them in the cache
type DefaultJSONSerializer struct {
}

// Marshal serializes the given value into a byte slice.
// @param v
func (d *DefaultJSONSerializer) Marshal(v any) ([]byte, error) {
	return json.Marshal(&v)
}

// Unmarshal deserializes the given byte slice into the given value.
// @param data
// @param v
func (d *DefaultJSONSerializer) Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, &v)
}
