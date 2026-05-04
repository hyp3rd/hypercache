package tests

import (
	"context"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/pkg/backend"
)

// TestDistMemory_HandlerCtxCancelsOnStop is the assertion that Phase 5d
// promised: a handler's operation ctx (s.ctx on the dist HTTP server)
// must observe Done() the instant Stop is called, even when the
// constructor ctx never cancels.
//
// Pre-5d the server captured the constructor ctx (typically
// context.Background()) — its Done() channel never fired, so handlers
// could not be aborted on Stop. Post-5d DistMemory derives lifeCtx from
// the constructor ctx with its own cancel, and Stop calls that cancel.
func TestDistMemory_HandlerCtxCancelsOnStop(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	addr := AllocatePort(t)

	bi, err := backend.NewDistMemory(ctx,
		backend.WithDistNode("life-test", addr),
		backend.WithDistReplication(1),
	)
	if err != nil {
		t.Fatalf("new dist memory: %v", err)
	}

	dm, ok := bi.(*backend.DistMemory)
	if !ok {
		t.Fatalf("expected *backend.DistMemory, got %T", bi)
	}

	// Capture the server-lifecycle ctx via the dist memory's accessor.
	lifeCtx := dm.LifecycleContext()

	// Sanity: lifecycle ctx exists and is not yet done.
	if lifeCtx == nil {
		t.Fatal("DistMemory lifecycle ctx is nil")
	}

	select {
	case <-lifeCtx.Done():
		t.Fatal("lifecycle ctx already canceled before Stop")
	default:
	}

	// Stop should cancel the lifecycle ctx promptly. Use a generous
	// shutdown deadline so we're not racing against fiber's drain.
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	stopErr := dm.Stop(shutdownCtx)
	if stopErr != nil {
		t.Fatalf("Stop: %v", stopErr)
	}

	// The lifecycle ctx must be done now — this is the property
	// pre-5d code didn't have.
	select {
	case <-lifeCtx.Done():
		// expected
	case <-time.After(time.Second):
		t.Fatal("lifecycle ctx did not cancel within 1s of Stop returning")
	}
}
