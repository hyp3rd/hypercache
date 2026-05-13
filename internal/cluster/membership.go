// Package cluster contains primitives for node identity, membership tracking and
// consistent hashing used by distributed backends.
package cluster

import (
	"sync"
	"time"
)

// StateChangeObserver is invoked AFTER a membership mutation
// (Upsert / Mark / Remove) commits, with the membership lock
// already released. Observers MUST NOT call back into the
// Membership they were registered on (deadlock); they SHOULD do
// minimal work (publish to a channel, increment a counter) and
// return promptly, since multiple observers run sequentially on
// the goroutine that performed the mutation.
//
// Phase C SSE: the cache binary registers one observer that
// publishes a `members` event onto the in-process event bus, so
// SSE subscribers see state transitions without re-deriving them
// by polling.
type StateChangeObserver func(id NodeID, state NodeState, version uint64)

// Membership tracks current cluster nodes (static MVP, future: gossip/swim).
type Membership struct {
	mu        sync.RWMutex
	nodes     map[NodeID]*Node
	ring      *Ring
	ver       MembershipVersion
	observers []StateChangeObserver
}

// NewMembership creates a new membership container bound to a ring.
func NewMembership(ring *Ring) *Membership { return &Membership{nodes: map[NodeID]*Node{}, ring: ring} }

// OnStateChange registers a callback invoked after every membership
// mutation (Upsert, Mark, Remove). Registration is append-only and
// not safe for concurrent use with mutations — call this once at
// construction before the cache starts driving heartbeats / gossip.
//
// The callback runs OUTSIDE the membership lock so a slow observer
// does not block the SWIM heartbeat loop. Observers fire in
// registration order; one panicking observer would skip every
// observer registered after it, so observer authors must recover
// in their own code (the package itself does not wrap with
// recover() because that would mask programming bugs).
func (m *Membership) OnStateChange(fn StateChangeObserver) {
	if fn == nil {
		return
	}

	m.mu.Lock()

	m.observers = append(m.observers, fn)
	m.mu.Unlock()
}

// Upsert adds or updates a node and rebuilds ring.
func (m *Membership) Upsert(n *Node) {
	m.mu.Lock()

	n.LastSeen = time.Now()
	m.nodes[n.ID] = n

	nodes := make([]*Node, 0, len(m.nodes))
	for _, v := range m.nodes { // exclude dead nodes from ring ownership
		if v.State != NodeDead {
			nodes = append(nodes, v)
		}
	}

	version := m.ver.Next()
	observers := m.observers
	id := n.ID
	state := n.State
	m.mu.Unlock()

	m.ring.Build(nodes)
	notify(observers, id, state, version)
}

// List returns current nodes snapshot.
func (m *Membership) List() []*Node {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]*Node, 0, len(m.nodes))
	for _, v := range m.nodes {
		if v == nil {
			continue
		}

		cp := *v

		out = append(out, &cp)
	}

	return out
}

// Ring returns the underlying ring reference.
func (m *Membership) Ring() *Ring { return m.ring }

// Remove deletes a node from membership and rebuilds the ring. Returns true if removed.
func (m *Membership) Remove(id NodeID) bool {
	m.mu.Lock()

	_, ok := m.nodes[id]
	if !ok {
		m.mu.Unlock()

		return false
	}

	delete(m.nodes, id)

	nodes := make([]*Node, 0, len(m.nodes))
	for _, v := range m.nodes { // exclude dead nodes
		if v.State != NodeDead {
			nodes = append(nodes, v)
		}
	}

	version := m.ver.Next()
	observers := m.observers
	m.mu.Unlock()

	m.ring.Build(nodes)
	// State NodeDead represents "removed from membership" for
	// observer purposes; the node is gone from the map and will
	// not appear in any subsequent List() call.
	notify(observers, id, NodeDead, version)

	return true
}

// Mark records an observer-side state update for the given node.
// Behavior depends on whether `state` represents a real transition:
//
//   - state == current: no-op. LastSeen refreshes (the peer answered
//     a heartbeat probe, so its observability window resets), but
//     incarnation, the membership version vector, and registered
//     observers are all left alone.
//   - state != current: incarnation bumps, version vector advances,
//     and observers fire — this is a real membership state change
//     and gossip-merge / SSE consumers need to know.
//
// Returns true if the node exists.
//
// Why no-op on same state: pre-fix every successful heartbeat probe
// called Mark(peer, Alive) and unconditionally bumped both the
// node's incarnation AND the membership version counter. Operators
// running a 5-node cluster for a few hours saw incarnations climb
// to ~2,378 and MembershipVersion past 4,800 — the value tracked
// "elapsed probes," not "state changes." SWIM defines incarnation
// as a sequence number OWNED by the node itself, and the
// membership-version vector should signal "something real
// happened" so gossip knows when to fan out a delta. Both must be
// quiet during steady-state liveness churn.
//
// Self-refute — the one observer-side path that legitimately needs
// to bump incarnation regardless of the local state value — lives
// in `Refute()` so the divergent semantic is explicit at the call
// site.
func (m *Membership) Mark(id NodeID, state NodeState) bool {
	m.mu.Lock()

	n, ok := m.nodes[id]
	if !ok {
		m.mu.Unlock()

		return false
	}

	if n.State == state {
		// Same-state no-op: refresh LastSeen so the suspect-after
		// timeout machinery sees the probe response, but do NOT
		// advance version / fire observers / bump incarnation.
		// Nothing materially changed.
		n.LastSeen = time.Now()
		m.mu.Unlock()

		return true
	}

	n.Incarnation++

	n.State = state
	n.LastSeen = time.Now()

	version := m.ver.Next()
	observers := m.observers

	m.mu.Unlock()

	notify(observers, id, state, version)

	return true
}

// Refute is the SWIM self-refute primitive: when this node receives
// gossip that it is Suspect/Dead at some incarnation N, it MUST
// publish a higher incarnation (N+1) with state=Alive so the next
// gossip tick disseminates the refutation cluster-wide (peers using
// "higher incarnation wins" adopt the new value).
//
// Unlike `Mark()`, Refute always bumps incarnation — the local state
// is already Alive (a node never thinks itself dead), so a
// transition-only rule would silently no-op and the refutation would
// fail to propagate. The dedicated method makes the divergent
// semantic obvious at the only call site that needs it
// (`refuteIfSuspected` in pkg/backend/dist_memory.go).
//
// Returns true if the node exists. State is unconditionally set to
// NodeAlive; the typical caller already knows the local node is
// alive but the explicit assignment guards against odd states the
// local view might be holding.
func (m *Membership) Refute(id NodeID) bool {
	m.mu.Lock()

	n, ok := m.nodes[id]
	if !ok {
		m.mu.Unlock()

		return false
	}

	n.Incarnation++

	n.State = NodeAlive
	n.LastSeen = time.Now()

	version := m.ver.Next()
	observers := m.observers

	m.mu.Unlock()

	notify(observers, id, NodeAlive, version)

	return true
}

// notify invokes each observer in registration order with the
// resolved state and version. Pulled out so the call sites read
// "mutate, unlock, notify" uniformly without inline loops.
func notify(observers []StateChangeObserver, id NodeID, state NodeState, version uint64) {
	for _, fn := range observers {
		fn(id, state, version)
	}
}

// Version returns current membership version.
func (m *Membership) Version() uint64 { return m.ver.Get() }
