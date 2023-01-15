package serializer

import "encoding/json"

type DefaultJSONSerializer struct {
}

// Serialize
// @param v
func (d *DefaultJSONSerializer) Serialize(v any) ([]byte, error) {
	return json.Marshal(v)
}

// Deserialize
// @param data
// @param v
func (d *DefaultJSONSerializer) Deserialize(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
