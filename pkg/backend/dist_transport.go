package backend

import (
	"context"
	"sync"

	"github.com/hyp3rd/hypercache/internal/sentinel"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// DistTransport defines forwarding operations needed by DistMemory.
type DistTransport interface {
	ForwardSet(ctx context.Context, nodeID string, item *cache.Item, replicate bool) error
	ForwardGet(ctx context.Context, nodeID, key string) (*cache.Item, bool, error)
	ForwardRemove(ctx context.Context, nodeID, key string, replicate bool) error
	Health(ctx context.Context, nodeID string) error
	// IndirectHealth asks `relayNodeID` to probe `targetNodeID` on the
	// caller's behalf. Used by the SWIM-style indirect-probe path: when
	// a direct probe to target fails, several relay nodes are asked to
	// probe target; if any of them succeeds, the target is alive and
	// the caller's local network was the issue, not the target.
	// Returns nil when the relay reports the target reachable.
	IndirectHealth(ctx context.Context, relayNodeID, targetNodeID string) error
	FetchMerkle(ctx context.Context, nodeID string) (*MerkleTree, error)
}

// InProcessTransport implements DistTransport for multiple DistMemory instances in the same process.
type InProcessTransport struct {
	mu       sync.RWMutex
	backends map[string]*DistMemory
}

// NewInProcessTransport creates a new empty transport.
func NewInProcessTransport() *InProcessTransport {
	return &InProcessTransport{backends: map[string]*DistMemory{}}
}

// Register adds backends; safe to call multiple times.
func (t *InProcessTransport) Register(b *DistMemory) {
	if b != nil && b.localNode != nil {
		t.mu.Lock()

		t.backends[string(b.localNode.ID)] = b
		t.mu.Unlock()
	}
}

// Unregister removes a backend (simulate failure in tests).
func (t *InProcessTransport) Unregister(id string) {
	t.mu.Lock()
	delete(t.backends, id)
	t.mu.Unlock()
}

// ForwardSet forwards a set operation to the specified backend node.
func (t *InProcessTransport) ForwardSet(ctx context.Context, nodeID string, item *cache.Item, replicate bool) error {
	b, ok := t.lookup(nodeID)
	if !ok {
		return sentinel.ErrBackendNotFound
	}

	// direct apply bypasses ownership check (already routed)
	b.applySet(ctx, item, replicate)

	return nil
}

// ForwardGet forwards a get operation to the specified backend node.
func (t *InProcessTransport) ForwardGet(_ context.Context, nodeID, key string) (*cache.Item, bool, error) {
	b, ok := t.lookup(nodeID)
	if !ok {
		return nil, false, sentinel.ErrBackendNotFound
	}

	it, ok2 := b.shardFor(key).items.GetCopy(key)
	if !ok2 {
		return nil, false, nil
	}

	return it, true, nil
}

// ForwardRemove forwards a remove operation.
func (t *InProcessTransport) ForwardRemove(ctx context.Context, nodeID, key string, replicate bool) error {
	b, ok := t.lookup(nodeID)
	if !ok {
		return sentinel.ErrBackendNotFound
	}

	b.applyRemove(ctx, key, replicate)

	return nil
}

// Health probes a backend.
func (t *InProcessTransport) Health(_ context.Context, nodeID string) error {
	if _, ok := t.lookup(nodeID); !ok {
		return sentinel.ErrBackendNotFound
	}

	return nil
}

// IndirectHealth asks the relay backend to probe target. In-process the
// relay's perspective on target is the same lookup table, so this is
// equivalent to a direct probe — tests that wire two InProcessTransport
// instances per cluster will exercise the relay-failure path naturally.
func (t *InProcessTransport) IndirectHealth(ctx context.Context, relayNodeID, targetNodeID string) error {
	relay, ok := t.lookup(relayNodeID)
	if !ok {
		return sentinel.ErrBackendNotFound
	}

	rt := relay.loadTransport()
	if rt == nil {
		return errNoTransport
	}

	return rt.Health(ctx, targetNodeID)
}

// FetchMerkle fetches a remote merkle tree.
func (t *InProcessTransport) FetchMerkle(_ context.Context, nodeID string) (*MerkleTree, error) {
	b, ok := t.lookup(nodeID)
	if !ok {
		return nil, sentinel.ErrBackendNotFound
	}

	return b.BuildMerkleTree(), nil
}

func (t *InProcessTransport) lookup(nodeID string) (*DistMemory, bool) {
	t.mu.RLock()

	b, ok := t.backends[nodeID]
	t.mu.RUnlock()

	return b, ok
}
