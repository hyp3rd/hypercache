// Package transport defines the network transport abstraction (client + codec) for
// the distributed backend. Concrete implementations (HTTP, gRPC, etc.) will
// satisfy the Client interface.
package transport

import (
	"context"
	"time"
)

// RPCError represents a transport-level error (placeholder for richer classification).
type RPCError struct {
	Op   string
	Node string
	Err  error
}

func (e *RPCError) Error() string { //nolint:ireturn
	if e == nil {
		return ""
	}

	return e.Op + "(" + e.Node + "): " + e.Err.Error()
}

// Client defines network transport operations needed by distributed backend.
// This abstracts over HTTP, gRPC, etc.
type Client interface {
	Get(ctx context.Context, node, key string) ([]byte, bool, error)
	Set(ctx context.Context, node, key string, value []byte, expiration time.Duration, replicate bool) error
	Remove(ctx context.Context, node, key string, replicate bool) error
	Health(ctx context.Context, node string) error
}

// Codec marshals/unmarshals values for transport (can reuse backend serializers).
type Codec interface {
	Marshal(v any) ([]byte, error)
	Unmarshal(data []byte, v any) error
}
