package tests

import (
	"context"
	"testing"

	"github.com/hyp3rd/hypercache/pkg/backend"
)

// newMerkleNode creates a single DistMemory node wired for merkle-sync tests
// — replication=1, chunk size 2, an ephemeral HTTP listener, and registered
// with the supplied transport. Construction uses context.Background() rather
// than a caller-supplied ctx because Stop runs from t.Cleanup at end-of-test
// where the test ctx may already be canceled — see StopOnCleanup for the
// same rationale.
func newMerkleNode(t *testing.T, transport *backend.InProcessTransport, id string) *backend.DistMemory {
	t.Helper()

	bi, err := backend.NewDistMemory(context.Background(),
		backend.WithDistNode(id, AllocatePort(t)),
		backend.WithDistReplication(1),
		backend.WithDistMerkleChunkSize(2),
	)
	if err != nil {
		t.Fatalf("new merkle node %s: %v", id, err)
	}

	b, ok := bi.(*backend.DistMemory)
	if !ok {
		t.Fatalf("expected *backend.DistMemory, got %T", bi)
	}

	StopOnCleanup(t, b)
	b.SetTransport(transport)
	transport.Register(b)

	return b
}
