package hypercache

import (
	"github.com/hyp3rd/hypercache/pkg/backend"
)

// DistMetrics returns distributed backend metrics if the underlying backend is DistMemory.
// Returns nil if unsupported.
func (hyperCache *HyperCache[T]) DistMetrics() any { // generic any to avoid exporting type into core interface
	if dm, ok := any(hyperCache.backend).(*backend.DistMemory); ok {
		m := dm.Metrics()

		return m
	}

	return nil
}

// ClusterOwners returns the owners for a key if the distributed backend supports it; otherwise empty slice.
func (hyperCache *HyperCache[T]) ClusterOwners(key string) []string {
	if dm, ok := any(hyperCache.backend).(*backend.DistMemory); ok {
		owners := dm.DebugOwners(key)

		out := make([]string, 0, len(owners))
		for _, o := range owners {
			out = append(out, string(o))
		}

		return out
	}

	return nil
}

// DistMembershipSnapshot returns a snapshot of membership if distributed backend; otherwise nil slice.
//
//nolint:nonamedreturns
func (hyperCache *HyperCache[T]) DistMembershipSnapshot() (members []struct {
	ID          string
	Address     string
	State       string
	Incarnation uint64
}, replication, vnodes int,
) {
	if dm, ok := any(hyperCache.backend).(*backend.DistMemory); ok {
		membership := dm.Membership()
		ring := dm.Ring()

		if membership == nil || ring == nil {
			return nil, 0, 0
		}

		nodes := membership.List()
		out := make([]struct {
			ID          string
			Address     string
			State       string
			Incarnation uint64
		}, 0, len(nodes))

		for _, node := range nodes {
			out = append(out, struct {
				ID          string
				Address     string
				State       string
				Incarnation uint64
			}{
				ID:          string(node.ID),
				Address:     node.Address,
				State:       node.State.String(),
				Incarnation: node.Incarnation,
			})
		}

		return out, ring.Replication(), ring.VirtualNodesPerNode()
	}

	return nil, 0, 0
}

// DistRingHashSpots returns vnode hashes as hex strings if available (debug).
func (hyperCache *HyperCache[T]) DistRingHashSpots() []string {
	if dm, ok := any(hyperCache.backend).(*backend.DistMemory); ok {
		if ring := dm.Ring(); ring != nil {
			return ring.VNodeHashes()
		}
	}

	return nil
}

// DistHeartbeatMetrics returns distributed heartbeat metrics if supported.
func (hyperCache *HyperCache[T]) DistHeartbeatMetrics() any {
	if dm, ok := any(hyperCache.backend).(*backend.DistMemory); ok {
		m := dm.Metrics()

		return map[string]any{
			"heartbeatSuccess":   m.HeartbeatSuccess,
			"heartbeatFailure":   m.HeartbeatFailure,
			"nodesRemoved":       m.NodesRemoved,
			"readPrimaryPromote": m.ReadPrimaryPromote,
		}
	}

	return nil
}
