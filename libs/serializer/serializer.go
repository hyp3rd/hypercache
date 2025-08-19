package serializer

import (
	"github.com/hyp3rd/ewrap"

	"github.com/hyp3rd/hypercache/errors"
)

// ISerializer is the interface that wraps the basic serializer methods.
type ISerializer interface {
	// Marshal serializes the given value into a byte slice.
	Marshal(v any) ([]byte, error)
	// Unmarshal deserializes the given byte slice into the given value.
	Unmarshal(data []byte, v any) error
}

var serializerRegistry = make(map[string]func() ISerializer)

// New returns a new serializer based on the serializerType.
func New(serializerType string) (ISerializer, error) {
	if serializerType == "" {
		return nil, ewrap.Wrap(errors.ErrParamCannotBeEmpty, "serializerType")
	}

	createFunc, ok := serializerRegistry[serializerType]
	if !ok {
		return nil, ewrap.Wrap(errors.ErrSerializerNotFound, serializerType)
	}

	return createFunc(), nil
}

// RegisterSerializer registers a new serializer with the given name.
func RegisterSerializer(serializerType string, createFunc func() ISerializer) {
	serializerRegistry[serializerType] = createFunc
}

func init() {
	// Register the default serializer.
	RegisterSerializer("default", func() ISerializer {
		return &DefaultJSONSerializer{}
	})

	// Register the msgpack serializer.
	RegisterSerializer("msgpack", func() ISerializer {
		return &MsgpackSerializer{}
	})
}
