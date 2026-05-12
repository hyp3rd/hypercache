package tests

import (
	"context"
	"testing"

	"github.com/hyp3rd/hypercache/internal/cluster"
	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// TestDistSet_PromotesOnGenericForwardError pins the resilience
// contract that `handleForwardPrimary` must promote to a replica
// owner on ANY non-nil forward error, not just the in-process
// transport's `ErrBackendNotFound` sentinel. Production HTTP/gRPC
// transports against a stopped container surface `net.OpError`,
// `io.EOF`, or `context.DeadlineExceeded` — none of which match
// the pre-fix promotion condition.
//
// Pre-fix behavior on this test: Set returns ErrChaosDrop (the
// forward fails, the default switch arm returns the error
// verbatim). Post-fix: Set returns nil and the key lands on the
// promoting node's local shard.
//
// Uses chaos hooks at DropRate=1.0 to deterministically force every
// outbound forward call to return ErrChaosDrop. Combined with
// ConsistencyOne writes so the local primary path satisfies quorum
// without needing any successful replica fan-out (chaos breaks the
// replica calls too — that's covered by TestDistChaos_*
// in tests/integration/, not what this test pins).
func TestDistSet_PromotesOnGenericForwardError(t *testing.T) {
	t.Parallel()

	chaos := backend.NewChaos()

	// 3 nodes, RF=3, ConsistencyOne. Chaos is wired onto every
	// node's transport; we only ever Set from one node, so only
	// that node's forwards exercise the promotion path.
	dc := SetupInProcessClusterRF(
		t, 3, 3,
		backend.WithDistChaos(chaos),
		backend.WithDistWriteConsistency(backend.ConsistencyOne),
	)

	a := dc.Nodes[0]
	b := dc.Nodes[1]
	c := dc.Nodes[2]

	// Pick a key whose primary is `b` and where `a` is a replica
	// owner. From `a`, the Set will forward to `b`; chaos drops
	// that call; promotion should fire because `a` is in
	// owners[1:].
	desired := []cluster.NodeID{b.LocalNodeID(), a.LocalNodeID(), c.LocalNodeID()}

	key, ok := FindOwnerKey(a, "promote-on-net-err-", desired, 5000)
	if !ok {
		t.Fatalf("could not find key with owner ordering [B, A, C]")
	}

	chaos.SetDropRate(1.0)

	err := a.Set(context.Background(), &cache.Item{Key: key, Value: "v1"})
	if err != nil {
		t.Fatalf("Set: got %v, want nil (promotion should have succeeded)", err)
	}

	if !a.LocalContains(key) {
		t.Errorf("LocalContains(%q) on promoting node: got false, want true", key)
	}

	if chaos.Drops() == 0 {
		t.Errorf("chaos.Drops: got 0, want > 0 (chaos didn't see the forward attempt)")
	}
}
