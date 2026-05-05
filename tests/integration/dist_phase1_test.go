package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// allocatePort listens on :0 then closes to get a free port.
func allocatePort(tb testing.TB) string {
	tb.Helper()

	var lc net.ListenConfig

	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("listen: %v", err)
	}

	addr := l.Addr().String()

	_ = l.Close()

	return addr
}

// makePhase1Node spins up a DistMemory backend wired for quorum reads/writes
// across three nodes — extracted so each subtest helper stays under the
// function-length budget.
func makePhase1Node(ctx context.Context, t *testing.T, id, addr string, seeds []string) *backend.DistMemory {
	t.Helper()

	bm, err := backend.NewDistMemory(
		ctx,
		backend.WithDistNode(id, addr),
		backend.WithDistSeeds(seeds),
		backend.WithDistReplication(3),
		backend.WithDistVirtualNodes(32),
		backend.WithDistHintReplayInterval(200*time.Millisecond),
		backend.WithDistHintTTL(5*time.Second),
		backend.WithDistReadConsistency(backend.ConsistencyQuorum),
		backend.WithDistWriteConsistency(backend.ConsistencyQuorum),
	)
	if err != nil {
		t.Fatalf("new dist memory: %v", err)
	}

	bk, ok := bm.(*backend.DistMemory)
	if !ok {
		t.Fatalf("expected *backend.DistMemory, got %T", bm)
	}

	return bk
}

// awaitNodeReplication polls node for key until value matches or deadline
// elapses; encodes the "give replication a moment" pattern without inlining
// goto-control-flow into the test body.
func awaitNodeReplication(ctx context.Context, node *backend.DistMemory, key string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if it, ok := node.Get(ctx, key); ok && valueOK(it.Value) {
			return true
		}

		time.Sleep(100 * time.Millisecond)
	}

	return false
}

// TestDistPhase1BasicQuorum verifies three-node quorum Set/Get over the
// HTTP transport. Originally a scaffolding test that t.Skipf'd when
// replication hadn't propagated to the third node ("hint replay not yet
// observable" — true at the time the test was written). Hint replay is
// now fully wired (TestHintedHandoffReplay, TestDistFailureRecovery),
// so the test now asserts strictly: every owner must hold the value
// within the 3 s deadline. A failure here means the HTTP-transport
// replication path regressed.
func TestDistPhase1BasicQuorum(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	addrA := allocatePort(t)
	addrB := allocatePort(t)
	addrC := allocatePort(t)

	nodeA := makePhase1Node(ctx, t, "A", addrA, []string{addrB, addrC})
	nodeB := makePhase1Node(ctx, t, "B", addrB, []string{addrA, addrC})
	nodeC := makePhase1Node(ctx, t, "C", addrC, []string{addrA, addrB})

	t.Cleanup(func() {
		_ = nodeA.Stop(ctx)
		_ = nodeB.Stop(ctx)
		_ = nodeC.Stop(ctx)
	})

	// allow some time for ring initialization
	time.Sleep(200 * time.Millisecond)

	item := &cache.Item{Key: "k1", Value: []byte("v1"), Expiration: 0, Version: 1, Origin: "A", LastUpdated: time.Now()}

	err := nodeA.Set(ctx, item)
	if err != nil {
		t.Fatalf("set: %v", err)
	}

	got, ok := nodeB.Get(ctx, "k1")
	if !ok {
		t.Fatalf("expected quorum read via B: not found")
	}

	assertValue(t, got.Value)

	// Replication must reach C within deadline; the previous skip-on-miss
	// branches were placeholders for pre-hint-replay behavior. Locally
	// this completes in ~500 ms across 20 runs.
	if !awaitNodeReplication(ctx, nodeC, "k1", 3*time.Second) {
		it, present := nodeC.Get(ctx, "k1")
		if !present {
			t.Fatalf("nodeC never received replicated value within 3s")
		}

		t.Fatalf("nodeC has wrong value after wait: %T %v", it.Value, it.Value)
	}
}

// matchV1Plain is the logical value tested by valueOK below.
const matchV1Plain = "v1"

// matchV1Base64 is the base64 encoding of "v1" — distributed transports
// sometimes round-trip values as base64 strings.
const matchV1Base64 = "djE=" // base64.StdEncoding.EncodeToString([]byte("v1"))

// matchesV1 reports whether s represents logical "v1" — either as the
// plain literal, the JSON-quoted literal, or a base64 form that decodes
// to "v1". Centralizes the encoding-tolerance logic that valueOK fans out
// over its supported value types.
func matchesV1(s string) bool {
	if s == matchV1Plain || s == "\""+matchV1Plain+"\"" {
		return true
	}

	if s == matchV1Base64 {
		b, err := base64.StdEncoding.DecodeString(s)

		return err == nil && string(b) == matchV1Plain
	}

	return false
}

// valueOK returns true if the stored value matches logical "v1" across supported encodings.
func valueOK(v any) bool {
	switch x := v.(type) {
	case []byte:
		return matchesV1(string(x))

	case string:
		return matchesV1(x)

	case json.RawMessage:
		if len(x) == 0 {
			return false
		}

		// Try as JSON string literal first; fall back to raw bytes.
		var s string

		err := json.Unmarshal(x, &s)
		if err == nil && matchesV1(s) {
			return true
		}

		return matchesV1(string(x))

	default:
		return matchesV1(fmt.Sprintf("%v", x))
	}
}

func assertValue(t *testing.T, v any) {
	t.Helper()

	if !valueOK(v) {
		t.Fatalf("unexpected value representation: %T %v", v, v)
	}
}
