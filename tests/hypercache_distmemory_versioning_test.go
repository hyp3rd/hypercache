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

// findOrderedThreeOwnerKey brute-forces a key whose ring ownership ordering
// is exactly [n1, n2, n3]. This makes the versioning-quorum test deterministic
// — without it, forwarding paths vary by hash and obscure the assertion.
func findOrderedThreeOwnerKey(node *backend.DistMemory, want []cluster.NodeID) string {
	return findOrderedThreeOwnerKeyPrefix(node, want, "k")
}

// findOrderedThreeOwnerKeyPrefix is the prefix-parameterized variant of
// findOrderedThreeOwnerKey — used by tests that want their fixture keys to
// share a common prefix for log readability.
func findOrderedThreeOwnerKeyPrefix(node *backend.DistMemory, want []cluster.NodeID, prefix string) string {
	for i := range 3000 {
		cand := fmt.Sprintf("%s%d", prefix, i)

		owners := node.DebugOwners(cand)
		if len(owners) != len(want) {
			continue
		}

		matches := true

		for j, id := range want {
			if owners[j] != id {
				matches = false

				break
			}
		}

		if matches {
			return cand
		}
	}

	return prefix
}

// TestDistMemoryVersioningQuorum ensures higher version wins and quorum enforcement works.
func TestDistMemoryVersioningQuorum(t *testing.T) { //nolint:paralleltest // mutates shared transport
	const interval = 10 * time.Millisecond

	dc := SetupInProcessCluster(
		t, 3,
		backend.WithDistReplication(3),
		backend.WithDistHeartbeat(interval, 0, 0),
		backend.WithDistReadConsistency(backend.ConsistencyQuorum),
		backend.WithDistWriteConsistency(backend.ConsistencyQuorum),
	)

	b1, b2, b3 := dc.Nodes[0], dc.Nodes[1], dc.Nodes[2]

	key := findOrderedThreeOwnerKey(b1, []cluster.NodeID{b1.LocalNodeID(), b2.LocalNodeID(), b3.LocalNodeID()})

	// Write key via primary.
	item1 := &cache.Item{Key: key, Value: "v1"}

	err := b1.Set(context.Background(), item1)
	if err != nil {
		t.Fatalf("initial set: %v", err)
	}

	// Simulate a concurrent stale write on b2 (lower version, different origin).
	itemStale := &cache.Item{Key: key, Value: "v0", Version: 0, Origin: "zzz"}
	b2.DebugDropLocal(key)
	b2.DebugInject(itemStale)

	// Read quorum from node3: should observe latest (v1) and repair b2.
	it, ok := b3.Get(context.Background(), key)
	if !ok {
		t.Fatalf("expected read ok")
	}

	if it.Value != "v1" {
		t.Fatalf("expected value v1, got %v", it.Value)
	}

	if it2, ok2 := b2.Get(context.Background(), key); !ok2 || it2.Value != "v1" {
		t.Fatalf("expected repaired value on b2")
	}

	// Simulate reduced acks: unregister one replica and perform write requiring quorum (2 of 3).
	dc.Transport.Unregister(string(b3.LocalNodeID()))

	item2 := &cache.Item{Key: key, Value: "v2"}

	err = b1.Set(context.Background(), item2)
	if err != nil && !errors.Is(err, sentinel.ErrQuorumFailed) {
		t.Fatalf("unexpected error after replica loss: %v", err)
	}
}
