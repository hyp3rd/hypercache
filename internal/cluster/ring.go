package cluster

import (
	"fmt"
	"sort"
	"sync"

	"github.com/cespare/xxhash/v2"
)

// Ring implements a consistent hashing ring with virtual nodes.
type Ring struct {
	mu          sync.RWMutex
	vnodes      []vnode
	vnPerNode   int
	replication int
}

type vnode struct {
	hash uint64
	nid  NodeID
}

// RingOption configures ring.
type RingOption func(*Ring)

// WithVirtualNodes sets the number of virtual nodes per physical node.
func WithVirtualNodes(n int) RingOption {
	return func(r *Ring) {
		if n > 0 {
			r.vnPerNode = n
		}
	}
}

// WithReplication sets the replication factor (number of owners per key).
func WithReplication(n int) RingOption {
	return func(r *Ring) {
		if n > 0 {
			r.replication = n
		}
	}
}

// NewRing creates ring.
const (
	defaultVirtualNodes = 64
)

// NewRing creates a ring with defaults overridden by options.
// NewRing constructs a new Ring applying provided options.
func NewRing(opts ...RingOption) *Ring {
	r := &Ring{vnPerNode: defaultVirtualNodes, replication: 1}
	for _, o := range opts {
		o(r)
	}

	return r
}

// Build rebuilds ring from nodes set (copy-on-write).
// Build rebuilds the ring using the supplied node list.
func (r *Ring) Build(nodes []*Node) {
	vn := make([]vnode, 0, len(nodes)*r.vnPerNode)
	for _, node := range nodes {
		base := []byte(node.ID)
		for i := 0; i < r.vnPerNode; i++ { //nolint:intrange
			// combine node id and vnode index (allocate new slice to avoid touching base backing array)
			buf := make([]byte, len(base)+1)
			copy(buf, base)

			buf[len(base)] = byte(i)

			hv := xxhash.Sum64(buf)

			vn = append(vn, vnode{hash: hv, nid: node.ID})
		}
	}

	sort.Slice(vn, func(i, j int) bool { return vn[i].hash < vn[j].hash })
	r.mu.Lock()

	r.vnodes = vn
	r.mu.Unlock()
}

// Lookup returns primary + (replication-1) replicas.
// Lookup returns the primary owner and (replication-1) replicas for a key.
func (r *Ring) Lookup(key string) []NodeID {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.vnodes) == 0 {
		return nil
	}

	target := xxhash.Sum64String(key)

	idx := sort.Search(len(r.vnodes), func(i int) bool { return r.vnodes[i].hash >= target })
	if idx == len(r.vnodes) {
		idx = 0
	}

	res := make([]NodeID, 0, r.replication)
	seen := make(map[NodeID]struct{})

	for i := 0; len(res) < r.replication && i < len(r.vnodes); i++ {
		vn := r.vnodes[(idx+i)%len(r.vnodes)]
		if _, ok := seen[vn.nid]; ok {
			continue
		}

		seen[vn.nid] = struct{}{}
		res = append(res, vn.nid)
	}

	return res
}

// Replication returns replication factor.
func (r *Ring) Replication() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.replication
}

// VirtualNodesPerNode returns configured virtual nodes per physical node.
func (r *Ring) VirtualNodesPerNode() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.vnPerNode
}

// VNodeHashes returns a copy of vnode hash values as hex strings (debug only).
func (r *Ring) VNodeHashes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]string, 0, len(r.vnodes))
	for _, v := range r.vnodes {
		out = append(out, fmt.Sprintf("%016x:%s", v.hash, v.nid))
	}

	return out
}
