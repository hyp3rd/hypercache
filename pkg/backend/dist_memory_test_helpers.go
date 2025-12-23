//go:build test

package backend

import (
	"context"
	"time"
)

// DisableHTTPForTest stops the internal HTTP server and clears transport (testing helper).
func (dm *DistMemory) DisableHTTPForTest(ctx context.Context) { //nolint:ireturn
	if dm.httpServer != nil {
		err := dm.httpServer.stop(ctx)
		if err != nil {
			_ = err
		} // ignored best-effort

		dm.httpServer = nil
	}

	dm.transport = nil
}

// EnableHTTPForTest restarts HTTP server & transport if nodeAddr is set (testing helper).
func (dm *DistMemory) EnableHTTPForTest(ctx context.Context) { //nolint:ireturn
	if dm.httpServer != nil || dm.nodeAddr == "" {
		return
	}

	server := newDistHTTPServer(dm.nodeAddr)

	err := server.start(ctx, dm)
	if err != nil {
		return
	}

	dm.httpServer = server

	resolver := func(nodeID string) (string, bool) {
		if dm.membership != nil {
			for _, n := range dm.membership.List() {
				if string(n.ID) == nodeID {
					return "http://" + n.Address, true
				}
			}
		}

		if dm.localNode != nil && string(dm.localNode.ID) == nodeID {
			return "http://" + dm.localNode.Address, true
		}

		return "", false
	}

	dm.transport = NewDistHTTPTransport(2*time.Second, resolver)
}

// HintedQueueSize returns number of queued hints for a node (testing helper).
func (dm *DistMemory) HintedQueueSize(nodeID string) int { //nolint:ireturn
	dm.hintsMu.Lock()
	defer dm.hintsMu.Unlock()

	if dm.hints == nil {
		return 0
	}

	return len(dm.hints[nodeID])
}

// StartHintReplayForTest forces starting hint replay loop (testing helper).
func (dm *DistMemory) StartHintReplayForTest(ctx context.Context) { //nolint:ireturn
	if dm.hintReplayInt <= 0 || dm.hintTTL <= 0 {
		return
	}

	if dm.hintStopCh != nil { // already running
		return
	}

	stopCh := make(chan struct{})

	dm.hintStopCh = stopCh
	go dm.hintReplayLoop(ctx, stopCh)
}

// ReplayHintsForTest triggers a single synchronous replay cycle (testing helper).
func (dm *DistMemory) ReplayHintsForTest(ctx context.Context) { dm.replayHints(ctx) }
