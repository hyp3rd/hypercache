package serializer

import (
	"github.com/hyp3rd/ewrap"

	"github.com/hyp3rd/hypercache/internal/sentinel"
)

// ISerializer is the interface that wraps the basic serializer methods.
type ISerializer interface {
	// Marshal serializes the given value into a byte slice.
	Marshal(v any) ([]byte, error)
	// Unmarshal deserializes the given byte slice into the given value.
	Unmarshal(data []byte, v any) error
}

// Registry manages serializer constructors.
type Registry struct {
	serializers map[string]func() ISerializer
}

// getDefaultSerializers returns the default set of serializers.
func getDefaultSerializers() map[string]func() ISerializer {
	return map[string]func() ISerializer{
		"default": func() ISerializer {
			return &DefaultJSONSerializer{}
		},
		"msgpack": func() ISerializer {
			return &MsgpackSerializer{}
		},
	}
}

// NewSerializerRegistry creates a new serializer registry with default serializers pre-registered.
func NewSerializerRegistry() *Registry {
	registry := &Registry{
		serializers: make(map[string]func() ISerializer),
	}
	// Register the default serializers
	for name, createFunc := range getDefaultSerializers() {
		registry.Register(name, createFunc)
	}

	return registry
}

// NewEmptySerializerRegistry creates a new serializer registry without default serializers.
// This is useful for testing or when you want to register only specific serializers.
func NewEmptySerializerRegistry() *Registry {
	return &Registry{
		serializers: make(map[string]func() ISerializer),
	}
}

// Register registers a new serializer with the given name.
func (r *Registry) Register(serializerType string, createFunc func() ISerializer) {
	r.serializers[serializerType] = createFunc
}

// New returns a new serializer based on the serializerType.
func (r *Registry) New(serializerType string) (ISerializer, error) {
	if serializerType == "" {
		return nil, ewrap.Wrap(sentinel.ErrParamCannotBeEmpty, "serializerType")
	}

	createFunc, ok := r.serializers[serializerType]
	if !ok {
		return nil, ewrap.Wrap(sentinel.ErrSerializerNotFound, serializerType)
	}

	return createFunc(), nil
}

// New returns a new serializer using a new registry instance with default serializers.
// The serializerType parameter is used to select the serializer from the default serializers.
func New(serializerType string) (ISerializer, error) {
	registry := NewSerializerRegistry()

	return registry.New(serializerType)
}
