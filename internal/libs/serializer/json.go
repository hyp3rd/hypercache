// Package serializer provides serialization interfaces and implementations for converting
// Go values to and from byte slices. This package is designed to work with caching systems
// that need to store and retrieve arbitrary Go values.
//
// The package includes a default JSON serializer implementation that uses the goccy/go-json
// library for efficient JSON marshaling and unmarshaling operations.
package serializer

import (
	"github.com/goccy/go-json"

	"github.com/hyp3rd/ewrap"
)

// DefaultJSONSerializer leverages the default `json` to serialize the items before storing them in the cache.
type DefaultJSONSerializer struct{}

// Marshal serializes the given value into a byte slice.
// @param v.
func (*DefaultJSONSerializer) Marshal(v any) ([]byte, error) { // receiver omitted (unused)
	data, err := json.Marshal(&v)
	if err != nil {
		return nil, ewrap.Wrap(err, "failed to marshal json")
	}

	return data, nil
}

// Unmarshal deserializes the given byte slice into the given value.
// @param data
// @param v.
func (*DefaultJSONSerializer) Unmarshal(data []byte, v any) error { // receiver omitted (unused)
	err := json.Unmarshal(data, &v)
	if err != nil {
		return ewrap.Wrap(err, "failed to unmarshal json")
	}

	return nil
}
