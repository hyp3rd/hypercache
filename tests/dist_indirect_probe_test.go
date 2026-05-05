//go:build test

package tests

import (
	"context"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/pkg/backend"
)

// TestDistIndirectProbe_ReachabilityViaRelay is the contract test for
// Phase B.1: when a target peer is reachable via the cluster, the
// indirect-probe path reports true and increments the success metric.
// Built on the in-process cluster helper (3 nodes) so the test is
// deterministic — node A asks node B to probe node C, all three are
// alive and registered with the same in-process transport, so B
// trivially reaches C.
//
// The companion negative case (`...UnreachableTargetReturnsFalse`)
// drives the same path with C unregistered to confirm the helper
// short-circuits to false rather than masking the failure.
func TestDistIndirectProbe_ReachabilityViaRelay(t *testing.T) {
	t.Parallel()

	dc := SetupInProcessClusterRF(
		t, 3, 3,
		backend.WithDistIndirectProbes(2, 50*time.Millisecond),
	)

	if len(dc.Nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(dc.Nodes))
	}

	a, c := dc.Nodes[0], dc.Nodes[2]

	// A asks the cluster to probe C; C is alive on the shared transport,
	// so at least one relay (B) will confirm reachability.
	if !a.IndirectProbeReachableForTest(context.Background(), string(c.LocalNodeID())) {
		t.Fatalf("expected indirect probe to confirm C reachable; got false")
	}

	if got := a.Metrics().IndirectProbeSuccess; got == 0 {
		t.Fatalf("expected IndirectProbeSuccess > 0 after successful relay, got %d", got)
	}
}

// TestDistIndirectProbe_UnreachableTargetReturnsFalse pairs with the
// reachability test: if every relay fails to reach the target, the
// helper must return false (so the caller can proceed to mark suspect)
// and the failure metric must increment.
//
// We simulate "target unreachable from every relay" by unregistering
// C from the shared in-process transport — relays look up C, miss,
// and bubble ErrBackendNotFound back up the indirect path.
func TestDistIndirectProbe_UnreachableTargetReturnsFalse(t *testing.T) {
	t.Parallel()

	dc := SetupInProcessClusterRF(
		t, 3, 3,
		backend.WithDistIndirectProbes(2, 50*time.Millisecond),
	)

	a, c := dc.Nodes[0], dc.Nodes[2]
	targetID := string(c.LocalNodeID())

	// Unregister C from the transport — every relay's Health(C) probe
	// will now miss, so no relay can confirm reachability.
	dc.Transport.Unregister(targetID)

	if a.IndirectProbeReachableForTest(context.Background(), targetID) {
		t.Fatalf("expected indirect probe to report C unreachable; got true")
	}

	if got := a.Metrics().IndirectProbeFailure; got == 0 {
		t.Fatalf("expected IndirectProbeFailure > 0 after every relay failed, got %d", got)
	}
}

// TestDistIndirectProbe_ZeroKeepsLegacyBehavior confirms the new
// indirect-probe option is fully opt-in: a default-constructed
// DistMemory (no WithDistIndirectProbes call) reports false from
// IndirectProbeReachableForTest because indirectProbeK is 0, and no
// indirect metrics increment. The pre-Phase-B behavior (direct probe
// alone decides liveness) is preserved.
func TestDistIndirectProbe_ZeroKeepsLegacyBehavior(t *testing.T) {
	t.Parallel()

	dc := SetupInProcessClusterRF(t, 3, 3) // no WithDistIndirectProbes

	a, c := dc.Nodes[0], dc.Nodes[2]

	if a.IndirectProbeReachableForTest(context.Background(), string(c.LocalNodeID())) {
		t.Fatalf("expected default-off behavior: helper reports false when indirectProbeK=0")
	}

	m := a.Metrics()
	if m.IndirectProbeSuccess != 0 || m.IndirectProbeFailure != 0 || m.IndirectProbeRefuted != 0 {
		t.Fatalf("expected zero indirect-probe metrics with K=0, got success=%d failure=%d refuted=%d",
			m.IndirectProbeSuccess, m.IndirectProbeFailure, m.IndirectProbeRefuted)
	}
}
