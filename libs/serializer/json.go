package serializer

import (
	"github.com/goccy/go-json"

	"github.com/hyp3rd/ewrap"
)

// DefaultJSONSerializer leverages the default `json` to serialize the items before storing them in the cache.
type DefaultJSONSerializer struct{}

// Marshal serializes the given value into a byte slice.
// @param v.
func (d *DefaultJSONSerializer) Marshal(v any) ([]byte, error) {
	data, err := json.Marshal(&v)
	if err != nil {
		return nil, ewrap.Wrap(err, "failed to marshal json")
	}

	return data, nil
}

// Unmarshal deserializes the given byte slice into the given value.
// @param data
// @param v.
func (d *DefaultJSONSerializer) Unmarshal(data []byte, v any) error {
	err := json.Unmarshal(data, &v)
	if err != nil {
		return ewrap.Wrap(err, "failed to unmarshal json")
	}

	return nil
}
