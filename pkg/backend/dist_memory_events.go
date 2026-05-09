package backend

import (
	"time"

	"github.com/hyp3rd/hypercache/internal/cluster"
	"github.com/hyp3rd/hypercache/internal/eventbus"
)

// memberWire is the per-node JSON shape published in `members`
// events. Field names + JSON tags match
// HyperCache.DistMembershipSnapshot exactly so the monitor's
// TanStack-Query cache can swap polled and pushed data
// interchangeably.
type memberWire struct {
	ID          string `json:"ID"`
	Address     string `json:"Address"`
	State       string `json:"State"`
	Incarnation uint64 `json:"Incarnation"`
}

// heartbeatPublishInterval is the cadence of `heartbeat` events
// pushed onto the bus. 1 Hz aligns with the SWIM heartbeat
// default and is dense enough for the monitor UI; per-counter
// publishing would flood the bus at hbInterval × N nodes.
const heartbeatPublishInterval = time.Second

// EventBus returns the in-process broadcaster for topology events.
// The management HTTP server's SSE handler subscribes here. Returns
// nil only on the (test-only) path where DistMemory was constructed
// without going through NewDistMemory.
func (dm *DistMemory) EventBus() *eventbus.Bus { return dm.eventBus }

// installEventBusObserver constructs the in-process event bus and
// registers a Membership observer that publishes a `members` event
// (full membership snapshot) on every state change. Idempotent —
// safe to call once per construction.
//
// The observer is registered AFTER the initial Upsert in
// initMembershipIfNeeded so the local-node-bootstrap mutation
// doesn't fire a synthetic event before any subscriber exists. The
// SSE handler sends an initial snapshot on connect anyway, so
// missing the bootstrap event is fine.
func (dm *DistMemory) installEventBusObserver() {
	if dm.eventBus != nil {
		return
	}

	dm.eventBus = eventbus.New()

	if dm.membership == nil {
		return
	}

	dm.membership.OnStateChange(func(_ cluster.NodeID, _ cluster.NodeState, _ uint64) {
		dm.eventBus.Publish(eventbus.Event{
			Type:    "members",
			Payload: dm.membersSnapshot(),
		})
	})
}

// startHeartbeatEventPublisher emits a `heartbeat` event on the
// in-process event bus once per heartbeatPublishInterval.
// Intentionally separate from the SWIM heartbeat loop:
//
//   - SWIM probes increment counters at hbInterval × N nodes;
//     publishing per-increment would flood the bus.
//   - A 1 Hz snapshot tick is dense enough for the monitor's UI
//     (the polling fallback was 2 Hz) without becoming a
//     bandwidth hog on long-lived SSE connections.
//   - Standalone mode (no hbInterval) still gets a heartbeat
//     stream, so /topology stays useful even on single-node dev.
//
// Stops on dm.lifeCtx cancellation (binary shutdown). Exits
// cleanly when no subscribers exist (publish is a no-op then).
func (dm *DistMemory) startHeartbeatEventPublisher() {
	if dm.eventBus == nil {
		return
	}

	go func() {
		ticker := time.NewTicker(heartbeatPublishInterval)
		defer ticker.Stop()

		for {
			select {
			case <-dm.lifeCtx.Done():
				return
			case <-ticker.C:
				dm.eventBus.Publish(eventbus.Event{
					Type:    "heartbeat",
					Payload: dm.heartbeatSnapshot(),
				})
			}
		}
	}()
}

// membersSnapshot builds the wire payload for a `members` event,
// matching the /cluster/members JSON shape exactly. Reflects the
// same projection logic HyperCache.DistMembershipSnapshot uses;
// reading directly here avoids a circular dep (HyperCache depends
// on this package).
func (dm *DistMemory) membersSnapshot() map[string]any {
	if dm.membership == nil || dm.ring == nil {
		return map[string]any{
			"replication":  0,
			"virtualNodes": 0,
			"members":      []memberWire{},
		}
	}

	nodes := dm.membership.List()
	wire := make([]memberWire, 0, len(nodes))

	for _, node := range nodes {
		wire = append(wire, memberWire{
			ID:          string(node.ID),
			Address:     node.Address,
			State:       node.State.String(),
			Incarnation: node.Incarnation,
		})
	}

	return map[string]any{
		"replication":  dm.ring.Replication(),
		"virtualNodes": dm.ring.VirtualNodesPerNode(),
		"members":      wire,
	}
}

// heartbeatSnapshot builds the wire payload for a `heartbeat`
// event. Matches the /cluster/heartbeat JSON shape (4 fields)
// produced by HyperCache.DistHeartbeatMetrics.
func (dm *DistMemory) heartbeatSnapshot() map[string]any {
	m := dm.Metrics()

	return map[string]any{
		"heartbeatSuccess":   m.HeartbeatSuccess,
		"heartbeatFailure":   m.HeartbeatFailure,
		"nodesRemoved":       m.NodesRemoved,
		"readPrimaryPromote": m.ReadPrimaryPromote,
	}
}
