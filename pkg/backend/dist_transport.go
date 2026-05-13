package backend

import (
	"context"
	"sync"
	"time"

	"github.com/hyp3rd/hypercache/internal/cluster"
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
	// Gossip pushes the caller's full member-list snapshot to
	// `targetNodeID`. The receiver merges it via higher-incarnation-
	// wins and self-refutes if the snapshot claims it is suspect.
	// Used by the cross-process gossip path; in-process clusters
	// short-circuit to a direct method call instead.
	Gossip(ctx context.Context, targetNodeID string, members []GossipMember) error
	FetchMerkle(ctx context.Context, nodeID string) (*MerkleTree, error)
	// ListKeys enumerates keys held on the remote node's shards,
	// optionally filtered by `pattern`. Empty pattern returns all
	// keys; a pattern containing any glob meta-character (* ? [) is
	// matched via path.Match, otherwise treated as a prefix.
	// Implementations walk the per-shard cursor pagination internally
	// and return the materialized key set to the caller; safe for
	// node-scale enumeration, capped at DistMemory.listKeysMax to
	// bound memory.
	ListKeys(ctx context.Context, nodeID, pattern string) ([]string, error)
}

// GossipMember is the wire-friendly snapshot of a cluster.Node used
// by the Gossip transport method. Stays a separate struct from
// cluster.Node so the wire schema doesn't drift when the cluster
// package adds internal fields.
type GossipMember struct {
	ID          string `json:"id"`
	Address     string `json:"address"`
	State       string `json:"state"`
	Incarnation uint64 `json:"incarnation"`
}

// nodesToGossipMembers projects a cluster.Node snapshot down to the
// wire shape. Nil entries are dropped — they shouldn't appear in
// practice but the projection is defensive.
func nodesToGossipMembers(nodes []*cluster.Node) []GossipMember {
	out := make([]GossipMember, 0, len(nodes))

	for _, n := range nodes {
		if n == nil {
			continue
		}

		out = append(out, GossipMember{
			ID:          string(n.ID),
			Address:     n.Address,
			State:       n.State.String(),
			Incarnation: n.Incarnation,
		})
	}

	return out
}

// gossipMembersToNodes inflates a wire-shape snapshot back into
// cluster.Node values for handoff to acceptGossip. Unknown state
// strings fall back to NodeAlive — the receiver's
// higher-incarnation-wins logic still applies, and a stuck-suspect
// claim from a peer running an older state vocabulary degrades
// gracefully to alive-at-this-incarnation.
func gossipMembersToNodes(members []GossipMember) []*cluster.Node {
	out := make([]*cluster.Node, 0, len(members))

	for _, m := range members {
		out = append(out, &cluster.Node{
			ID:          cluster.NodeID(m.ID),
			Address:     m.Address,
			State:       parseGossipState(m.State),
			Incarnation: m.Incarnation,
			LastSeen:    time.Now(),
		})
	}

	return out
}

// parseGossipState maps the wire state string back to the
// internal NodeState enum. "alive" and unknown values both
// resolve to NodeAlive (defensive — see gossipMembersToNodes);
// the explicit "alive" branch is omitted to satisfy the
// identical-switch-branches lint while keeping the same
// semantic.
func parseGossipState(s string) cluster.NodeState {
	switch s {
	case "suspect":
		return cluster.NodeSuspect
	case "dead":
		return cluster.NodeDead
	default:
		return cluster.NodeAlive
	}
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

	// Forwarded arrival: ownership guard fires if the receiver's
	// ring view says this node isn't an owner of the key. Stops
	// divergent-view senders from planting stuck keys.
	b.applyForwardedSet(ctx, item, replicate)

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

// Gossip delivers the snapshot directly to the target backend's
// acceptGossip — this is the in-process equivalent of the HTTP
// `/internal/gossip` endpoint, with the type translation done
// inline so the rest of the SWIM machinery can stay agnostic to
// transport choice.
func (t *InProcessTransport) Gossip(_ context.Context, targetNodeID string, members []GossipMember) error {
	target, ok := t.lookup(targetNodeID)
	if !ok {
		return sentinel.ErrBackendNotFound
	}

	target.acceptGossip(gossipMembersToNodes(members))

	return nil
}

// FetchMerkle fetches a remote merkle tree.
func (t *InProcessTransport) FetchMerkle(_ context.Context, nodeID string) (*MerkleTree, error) {
	b, ok := t.lookup(nodeID)
	if !ok {
		return nil, sentinel.ErrBackendNotFound
	}

	return b.BuildMerkleTree(), nil
}

// ListKeys enumerates the remote node's local shards, optionally
// filtered by pattern. In-process means we have direct access to
// the target's shards — no HTTP roundtrip, no cursor walking, just
// a synchronous in-memory scan.
func (t *InProcessTransport) ListKeys(_ context.Context, nodeID, pattern string) ([]string, error) {
	b, ok := t.lookup(nodeID)
	if !ok {
		return nil, sentinel.ErrBackendNotFound
	}

	matcher, err := buildKeyMatcher(pattern)
	if err != nil {
		return nil, err
	}

	return b.localMatchingKeys(matcher), nil
}

func (t *InProcessTransport) lookup(nodeID string) (*DistMemory, bool) {
	t.mu.RLock()

	b, ok := t.backends[nodeID]
	t.mu.RUnlock()

	return b, ok
}
