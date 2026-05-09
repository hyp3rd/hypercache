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

// Mark updates node state + incarnation and refreshes LastSeen. Returns true if node exists.
func (m *Membership) Mark(id NodeID, state NodeState) bool {
	m.mu.Lock()

	n, ok := m.nodes[id]
	if !ok {
		m.mu.Unlock()

		return false
	}

	n.State = state
	n.Incarnation++

	n.LastSeen = time.Now()

	version := m.ver.Next()
	observers := m.observers

	m.mu.Unlock()

	notify(observers, id, state, version)

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
