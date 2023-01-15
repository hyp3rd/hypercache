package serializer

type ISerializer interface {
	Serialize(v any) ([]byte, error)
	Deserialize(data []byte, v any) error
}

func New() ISerializer {
	return &DefaultJSONSerializer{}
}
