package tests

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/internal/cluster"
	"github.com/hyp3rd/hypercache/internal/sentinel"
	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// TestWriteQuorumSuccess ensures a QUORUM write succeeds with majority acks.
func TestWriteQuorumSuccess(t *testing.T) {
	t.Parallel()

	dc := SetupInProcessClusterRF(
		t, 3, 2,
		backend.WithDistReplication(2),
		backend.WithDistWriteConsistency(backend.ConsistencyQuorum),
	)

	item := &cache.Item{Key: "k1", Value: "v1"}

	err := dc.Nodes[0].Set(context.Background(), item)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	metrics := dc.Nodes[0].Metrics()
	if metrics.WriteAttempts < 1 {
		t.Fatalf("expected WriteAttempts >=1, got %d", metrics.WriteAttempts)
	}

	if metrics.WriteQuorumFailures != 0 {
		t.Fatalf("unexpected WriteQuorumFailures: %d", metrics.WriteQuorumFailures)
	}
}

// TestWriteQuorumFailure ensures ALL consistency fails when not enough acks.
func TestWriteQuorumFailure(t *testing.T) {
	t.Parallel()

	dc := SetupInProcessCluster(
		t, 3,
		backend.WithDistReplication(3),
		backend.WithDistWriteConsistency(backend.ConsistencyAll),
		backend.WithDistHintTTL(time.Minute),
		backend.WithDistHintReplayInterval(50*time.Millisecond),
	)

	// Force ALL-consistency failure by dropping the third node from the
	// transport — its acks will never arrive, so the write must fail.
	thirdNode := dc.Nodes[2]
	dc.Transport.Unregister(string(thirdNode.LocalNodeID()))

	// Pick a key with all three nodes as owners (replication=3 guarantees
	// it but ordering varies — pick one with the first node as primary for
	// log clarity).
	key := pickAllOwnerKey(dc.Nodes[0], dc.Nodes[0].LocalNodeID(), 50)

	item := &cache.Item{Key: key, Value: "v-fail"}

	err := dc.Nodes[0].Set(context.Background(), item)
	if !errors.Is(err, sentinel.ErrQuorumFailed) {
		owners := dc.Ring.Lookup(key)

		ids := make([]string, 0, len(owners))
		for _, o := range owners {
			ids = append(ids, string(o))
		}

		t.Fatalf("expected ErrQuorumFailed, got %v (owners=%v)", err, ids)
	}

	metrics := dc.Nodes[0].Metrics()
	if metrics.WriteQuorumFailures < 1 {
		t.Fatalf("expected WriteQuorumFailures >=1, got %d", metrics.WriteQuorumFailures)
	}

	if metrics.WriteAttempts < 1 {
		t.Fatalf("expected WriteAttempts >=1, got %d", metrics.WriteAttempts)
	}
}

// pickAllOwnerKey scans candidate keys until it finds one with the desired
// primary owner — used by quorum tests to keep the assertion clear.
func pickAllOwnerKey(node *backend.DistMemory, wantPrimary cluster.NodeID, attempts int) string {
	const fallback = "quorum-all-fail"

	for i := range attempts {
		candidate := fmt.Sprintf("quorum-all-fail-%d", i)

		owners := node.Ring().Lookup(candidate)
		if len(owners) == 3 && owners[0] == wantPrimary {
			return candidate
		}
	}

	return fallback
}
