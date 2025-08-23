//go:build test

package backend

import "context"

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

	dm.hintStopCh = make(chan struct{})
	go dm.hintReplayLoop(ctx)
}

// ReplayHintsForTest triggers a single synchronous replay cycle (testing helper).
func (dm *DistMemory) ReplayHintsForTest(ctx context.Context) { dm.replayHints(ctx) }
