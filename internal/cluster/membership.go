// Package cluster contains primitives for node identity, membership tracking and
// consistent hashing used by distributed backends.
package cluster

import (
	"sync"
	"time"
)

// Membership tracks current cluster nodes (static MVP, future: gossip/swim).
type Membership struct {
	mu    sync.RWMutex
	nodes map[NodeID]*Node
	ring  *Ring
}

// NewMembership creates a new membership container bound to a ring.
func NewMembership(ring *Ring) *Membership { return &Membership{nodes: map[NodeID]*Node{}, ring: ring} }

// Upsert adds or updates a node and rebuilds ring.
func (m *Membership) Upsert(n *Node) {
	m.mu.Lock()

	n.LastSeen = time.Now()
	m.nodes[n.ID] = n

	nodes := make([]*Node, 0, len(m.nodes))
	for _, v := range m.nodes {
		nodes = append(nodes, v)
	}

	m.mu.Unlock()
	m.ring.Build(nodes)
}

// List returns current nodes snapshot.
func (m *Membership) List() []*Node {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]*Node, 0, len(m.nodes))
	for _, v := range m.nodes {
		out = append(out, v)
	}

	return out
}

// Ring returns the underlying ring reference.
func (m *Membership) Ring() *Ring { return m.ring }

// Remove deletes a node from membership and rebuilds the ring. Returns true if removed.
func (m *Membership) Remove(id NodeID) bool { //nolint:ireturn
	m.mu.Lock()

	_, ok := m.nodes[id]
	if !ok {
		m.mu.Unlock()

		return false
	}

	delete(m.nodes, id)

	nodes := make([]*Node, 0, len(m.nodes))
	for _, v := range m.nodes { // collect snapshot
		nodes = append(nodes, v)
	}

	m.mu.Unlock()
	m.ring.Build(nodes)

	return true
}

// Mark updates node state + incarnation and refreshes LastSeen. Returns true if node exists.
func (m *Membership) Mark(id NodeID, state NodeState) bool { //nolint:ireturn
	m.mu.Lock()

	n, ok := m.nodes[id]
	if ok {
		n.State = state
		n.Incarnation++

		n.LastSeen = time.Now()
	}

	m.mu.Unlock()

	return ok
}
