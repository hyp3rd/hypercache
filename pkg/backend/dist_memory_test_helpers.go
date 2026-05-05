//go:build test

package backend

import (
	"context"
)

// DisableHTTPForTest stops the internal HTTP server and clears transport (testing helper).
func (dm *DistMemory) DisableHTTPForTest(ctx context.Context) {
	if dm.httpServer != nil {
		err := dm.httpServer.stop(ctx)
		if err != nil {
			_ = err
		} // ignored best-effort

		dm.httpServer = nil
	}

	dm.storeTransport(nil)
}

// EnableHTTPForTest restarts HTTP server & transport if nodeAddr is set (testing helper).
func (dm *DistMemory) EnableHTTPForTest(ctx context.Context) {
	if dm.httpServer != nil || dm.nodeAddr == "" {
		return
	}

	limits := dm.httpLimits.withDefaults()

	server := newDistHTTPServer(dm.nodeAddr, limits, dm.httpAuth)

	server.ctx = dm.lifeCtx // handler-side cancellation tied to Stop
	server.logger = dm.logger

	err := server.start(ctx, dm)
	if err != nil {
		return
	}

	dm.httpServer = server

	resolver := dm.makePeerURLResolver(limits)

	dm.storeTransport(NewDistHTTPTransportWithAuth(limits, dm.httpAuth, resolver))
}

// HintedQueueSize returns number of queued hints for a node (testing helper).
func (dm *DistMemory) HintedQueueSize(nodeID string) int {
	dm.hintsMu.Lock()
	defer dm.hintsMu.Unlock()

	if dm.hints == nil {
		return 0
	}

	return len(dm.hints[nodeID])
}

// StartHintReplayForTest forces starting hint replay loop (testing helper).
func (dm *DistMemory) StartHintReplayForTest(ctx context.Context) {
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
