package backend

import (
	"context"

	"github.com/hyp3rd/hypercache/internal/sentinel"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// DistTransport defines forwarding operations needed by DistMemory.
type DistTransport interface {
	ForwardSet(ctx context.Context, nodeID string, item *cache.Item, replicate bool) error
	ForwardGet(ctx context.Context, nodeID, key string) (*cache.Item, bool, error)
	ForwardRemove(ctx context.Context, nodeID, key string, replicate bool) error
	Health(ctx context.Context, nodeID string) error
	FetchMerkle(ctx context.Context, nodeID string) (*MerkleTree, error)
}

// InProcessTransport implements DistTransport for multiple DistMemory instances in the same process.
type InProcessTransport struct{ backends map[string]*DistMemory }

// NewInProcessTransport creates a new empty transport.
func NewInProcessTransport() *InProcessTransport { //nolint:ireturn
	return &InProcessTransport{backends: map[string]*DistMemory{}}
}

// Register adds backends; safe to call multiple times.
func (t *InProcessTransport) Register(b *DistMemory) {
	if b != nil && b.localNode != nil {
		t.backends[string(b.localNode.ID)] = b
	}
}

// Unregister removes a backend (simulate failure in tests).
func (t *InProcessTransport) Unregister(id string) { delete(t.backends, id) }

// ForwardSet forwards a set operation to the specified backend node.
func (t *InProcessTransport) ForwardSet(ctx context.Context, nodeID string, item *cache.Item, replicate bool) error { //nolint:ireturn
	b, ok := t.backends[nodeID]
	if !ok {
		return sentinel.ErrBackendNotFound
	}
	// direct apply bypasses ownership check (already routed)
	b.applySet(ctx, item, replicate)

	return nil
}

// ForwardGet forwards a get operation to the specified backend node.
func (t *InProcessTransport) ForwardGet(_ context.Context, nodeID, key string) (*cache.Item, bool, error) { //nolint:ireturn
	b, ok := t.backends[nodeID]
	if !ok {
		return nil, false, sentinel.ErrBackendNotFound
	}

	it, ok2 := b.shardFor(key).items.Get(key)
	if !ok2 {
		return nil, false, nil
	}

	return it, true, nil
}

// ForwardRemove forwards a remove operation.
func (t *InProcessTransport) ForwardRemove(ctx context.Context, nodeID, key string, replicate bool) error { //nolint:ireturn
	b, ok := t.backends[nodeID]
	if !ok {
		return sentinel.ErrBackendNotFound
	}

	b.applyRemove(ctx, key, replicate)

	return nil
}

// Health probes a backend.
func (t *InProcessTransport) Health(_ context.Context, nodeID string) error { //nolint:ireturn
	if _, ok := t.backends[nodeID]; !ok {
		return sentinel.ErrBackendNotFound
	}

	return nil
}

// FetchMerkle fetches a remote merkle tree.
func (t *InProcessTransport) FetchMerkle(_ context.Context, nodeID string) (*MerkleTree, error) { //nolint:ireturn
	b, ok := t.backends[nodeID]
	if !ok {
		return nil, sentinel.ErrBackendNotFound
	}

	return b.BuildMerkleTree(), nil
}
