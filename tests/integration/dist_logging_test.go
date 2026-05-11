package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/pkg/backend"
)

// TestDistLogging_NodeStartupAnnouncesClusterShape pins the most
// operator-visible event in the cluster lifecycle: every node startup
// emits ONE structured "cluster join: node starting" record summarizing
// the cluster shape it's about to join (node_id, address, replication,
// virtual_nodes, peer count, key intervals). Without this, post-mortem
// analysis of a misbehaving cluster has to reconstruct configuration
// from runtime metrics — the join log captures it at the source.
//
// Also asserts at least one background-loop "started" record appears.
// The exact loops depend on the test setup (heartbeat is on, gossip
// is off in this minimal config); we don't enumerate them strictly to
// keep the assertion robust against orthogonal future loop additions.
func TestDistLogging_NodeStartupAnnouncesClusterShape(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	addr := allocatePort(t)

	buf := newSyncBuffer()
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	bm, err := backend.NewDistMemory(
		ctx,
		backend.WithDistNode("logging-A", addr),
		backend.WithDistReplication(1),
		backend.WithDistVirtualNodes(8),
		backend.WithDistHeartbeat(200*time.Millisecond, 1*time.Second, 3*time.Second),
		backend.WithDistLogger(logger),
	)
	if err != nil {
		t.Fatalf("NewDistMemory: %v", err)
	}

	t.Cleanup(func() {
		dm, ok := bm.(*backend.DistMemory)
		if ok && dm != nil {
			_ = dm.Stop(ctx)
		}
	})

	// Give the loops a moment to log their start lines (they fire
	// synchronously from the constructor, but JSON encoding is async
	// via slog's handler so we wait briefly before reading).
	time.Sleep(50 * time.Millisecond)

	records := parseLogRecords(t, buf.String())

	join := findRecord(records, "cluster join: node starting")
	if join == nil {
		t.Fatalf("expected `cluster join: node starting` record; got:\n%s", buf.String())
	}

	assertClusterJoinFields(t, join)

	if !anyLoopStarted(records) {
		t.Errorf("expected at least one `* loop started` record; got:\n%s", buf.String())
	}
}

// assertClusterJoinFields verifies the cluster-join record carries the
// expected node_id / replication / virtual_nodes triple. Kept as a
// helper to hold the parent test under the cognitive-complexity cap.
func assertClusterJoinFields(t *testing.T, join map[string]any) {
	t.Helper()

	nodeID, ok := join["node_id"].(string)
	if !ok || nodeID != "logging-A" {
		t.Errorf("node_id: want logging-A, got %v", join["node_id"])
	}

	replication, ok := join["replication"].(float64)
	if !ok || replication != 1 {
		t.Errorf("replication: want 1, got %v", join["replication"])
	}

	vnodes, ok := join["virtual_nodes"].(float64)
	if !ok || vnodes != 8 {
		t.Errorf("virtual_nodes: want 8, got %v", join["virtual_nodes"])
	}
}

// anyLoopStarted returns true if any record's msg contains
// "loop started" — the loose match keeps the assertion robust
// against future loop additions.
func anyLoopStarted(records []map[string]any) bool {
	for _, r := range records {
		msg, ok := r["msg"].(string)
		if ok && strings.Contains(msg, "loop started") {
			return true
		}
	}

	return false
}

// TestDistLogging_AddPeerEmitsMembershipLogRecord pins that AddPeer
// produces a membership log line — previously silent, which made
// observing dynamic cluster joins from logs impossible.
func TestDistLogging_AddPeerEmitsMembershipLogRecord(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	addr := allocatePort(t)

	buf := newSyncBuffer()
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	bm, err := backend.NewDistMemory(
		ctx,
		backend.WithDistNode("logging-A", addr),
		backend.WithDistReplication(1),
		backend.WithDistLogger(logger),
	)
	if err != nil {
		t.Fatalf("NewDistMemory: %v", err)
	}

	dm, ok := bm.(*backend.DistMemory)
	if !ok || dm == nil {
		t.Fatalf("NewDistMemory: expected *backend.DistMemory, got %T", bm)
	}

	t.Cleanup(func() { _ = dm.Stop(ctx) })

	// Clear the buffer of startup logs so the assertion below is
	// scoped to AddPeer's emission only.
	buf.Reset()

	dm.AddPeer("peer-B:7946")

	rec := findRecord(parseLogRecords(t, buf.String()), "peer added to membership")
	if rec == nil {
		t.Fatalf("expected `peer added to membership` record; got:\n%s", buf.String())
	}

	if rec["peer_addr"] != "peer-B:7946" {
		t.Errorf("peer_addr: want peer-B:7946, got %v", rec["peer_addr"])
	}
}

// Helpers — kept local to this test file to avoid polluting the
// integration package's shared surface with logging-test-only types.

// syncBuffer is a bytes.Buffer guarded by a mutex so concurrent
// slog handler writes don't race with the test's reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func newSyncBuffer() *syncBuffer { return &syncBuffer{} }

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	n, err := b.buf.Write(p)
	if err != nil {
		return n, fmt.Errorf("syncBuffer write: %w", err)
	}

	return n, nil
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.buf.Reset()
}

func parseLogRecords(t *testing.T, s string) []map[string]any {
	t.Helper()

	out := []map[string]any{}
	for line := range strings.SplitSeq(s, "\n") {
		if line == "" {
			continue
		}

		var rec map[string]any

		err := json.Unmarshal([]byte(line), &rec)
		if err != nil {
			t.Fatalf("malformed log line: %q (%v)", line, err)
		}

		out = append(out, rec)
	}

	return out
}

func findRecord(records []map[string]any, msgPrefix string) map[string]any {
	for _, r := range records {
		msg, ok := r["msg"].(string)
		if ok && strings.HasPrefix(msg, msgPrefix) {
			return r
		}
	}

	return nil
}
