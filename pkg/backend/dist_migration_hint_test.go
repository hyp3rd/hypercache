package backend

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hyp3rd/ewrap"

	"github.com/hyp3rd/hypercache/internal/cluster"
	"github.com/hyp3rd/hypercache/internal/sentinel"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// Static sentinels for the scriptedTransport — err113 forbids defining
// dynamic errors with ewrap.New inside test bodies.
var (
	errScriptedNotUsed  = ewrap.New("scriptedTransport: method not exercised by this test")
	errScriptedSimulate = ewrap.New("scriptedTransport: simulated transport error")
)

const migrationHintTestOrigin = "test-A"

// scriptedTransport is a DistTransport stub that returns whatever the
// test sets on its forwardSetErr field. Keeps the test focused on
// hint-source bookkeeping without involving the real HTTP or
// in-process transport paths.
type scriptedTransport struct {
	forwardSetErr   atomic.Value // error or nil
	forwardSetCalls atomic.Int64
}

func (s *scriptedTransport) ForwardSet(_ context.Context, _ string, _ *cache.Item, _ bool) error {
	s.forwardSetCalls.Add(1)

	if v := s.forwardSetErr.Load(); v != nil {
		if err, ok := v.(error); ok {
			return err
		}
	}

	return nil
}

func (*scriptedTransport) ForwardGet(_ context.Context, _, _ string) (*cache.Item, bool, error) {
	return nil, false, nil
}

func (*scriptedTransport) ForwardRemove(_ context.Context, _, _ string, _ bool) error {
	return nil
}

func (*scriptedTransport) Health(_ context.Context, _ string) error {
	return nil
}

func (*scriptedTransport) IndirectHealth(_ context.Context, _, _ string) error {
	return nil
}

func (*scriptedTransport) Gossip(_ context.Context, _ string, _ []GossipMember) error {
	return nil
}

func (*scriptedTransport) FetchMerkle(_ context.Context, _ string) (*MerkleTree, error) {
	return nil, errScriptedNotUsed
}

func (*scriptedTransport) ListKeys(_ context.Context, _, _ string) ([]string, error) {
	return nil, errScriptedNotUsed
}

// newMigrationHintTestDM builds a DistMemory with just enough wiring to
// drive queueHint + processHint directly. We bypass NewDistMemory to
// avoid spinning a real cluster — the goal is to exercise the
// source-aware counters in isolation. Hint TTL is set to one minute
// (large enough that no test cares about expiry except the one that
// explicitly constructs an entry whose expire is in the past).
func newMigrationHintTestDM(t *testing.T) (*DistMemory, *scriptedTransport) {
	t.Helper()

	transport := &scriptedTransport{}
	dm := &DistMemory{
		logger:    slog.New(slog.DiscardHandler),
		hintTTL:   time.Minute,
		localNode: cluster.NewNode(migrationHintTestOrigin, "127.0.0.1:0"),
	}
	dm.storeTransport(transport)

	return dm, transport
}

// TestMigrationHint_QueueTagsSource verifies that queueHint tags each
// entry with the source it was queued from and bumps the matching
// per-source counter alongside the aggregate. Without this contract,
// every downstream replay-time accounting decision (clean / dirty /
// migration-only metrics) would fall apart.
func TestMigrationHint_QueueTagsSource(t *testing.T) {
	t.Parallel()

	dm, _ := newMigrationHintTestDM(t)
	item := &cache.Item{Key: "k1", Value: []byte("v"), Version: 1, Origin: migrationHintTestOrigin}

	dm.queueHint("peer-B", item, hintSourceMigration)
	dm.queueHint("peer-B", item, hintSourceReplication)

	if got := dm.metrics.hintedQueued.Load(); got != 2 {
		t.Errorf("aggregate hintedQueued: want 2, got %d", got)
	}

	if got := dm.metrics.migrationHintQueued.Load(); got != 1 {
		t.Errorf("migrationHintQueued: want 1, got %d", got)
	}

	dm.hintsMu.Lock()

	queue := dm.hints["peer-B"]
	dm.hintsMu.Unlock()

	if len(queue) != 2 {
		t.Fatalf("queue length: want 2, got %d", len(queue))
	}

	if queue[0].source != hintSourceMigration {
		t.Errorf("queue[0].source: want hintSourceMigration, got %d", queue[0].source)
	}

	if queue[1].source != hintSourceReplication {
		t.Errorf("queue[1].source: want hintSourceReplication, got %d", queue[1].source)
	}
}

// TestMigrationHint_ReplaySuccessUpdatesPerSourceCounters drives
// processHint manually through its happy path and confirms migration
// hints get tallied separately, replication hints do not bump the
// migration counter, and last_age_ns moves to a non-zero value.
func TestMigrationHint_ReplaySuccessUpdatesPerSourceCounters(t *testing.T) {
	t.Parallel()

	dm, _ := newMigrationHintTestDM(t)
	queuedAt := time.Now().Add(-3 * time.Second) // 3s residency
	item := &cache.Item{Key: "k1", Value: []byte("v"), Version: 1, Origin: migrationHintTestOrigin}

	migEntry := hintedEntry{
		item:     item,
		queuedAt: queuedAt,
		expire:   queuedAt.Add(time.Minute),
		source:   hintSourceMigration,
	}

	repEntry := hintedEntry{
		item:     item,
		queuedAt: queuedAt,
		expire:   queuedAt.Add(time.Minute),
		source:   hintSourceReplication,
	}

	now := time.Now()

	if action := dm.processHint(context.Background(), "peer-B", migEntry, now); action != 1 {
		t.Errorf("migration replay: want action=1 (remove), got %d", action)
	}

	if action := dm.processHint(context.Background(), "peer-B", repEntry, now); action != 1 {
		t.Errorf("replication replay: want action=1 (remove), got %d", action)
	}

	if got := dm.metrics.hintedReplayed.Load(); got != 2 {
		t.Errorf("aggregate hintedReplayed: want 2, got %d", got)
	}

	if got := dm.metrics.migrationHintReplayed.Load(); got != 1 {
		t.Errorf("migrationHintReplayed: want 1 (migration only), got %d", got)
	}

	ageNs := dm.metrics.migrationHintLastAgeNanos.Load()
	if ageNs <= 0 {
		t.Errorf("migrationHintLastAgeNanos: want > 0 after migration replay, got %d", ageNs)
	}

	// The recorded age should be roughly 3 seconds (the queuedAt offset).
	// Allow generous slack to absorb test-machine scheduling jitter.
	if ageNs < int64(2*time.Second) || ageNs > int64(10*time.Second) {
		t.Errorf("migrationHintLastAgeNanos: want ~3s residency, got %v", time.Duration(ageNs))
	}
}

// TestMigrationHint_ExpiredBumpsPerSourceCounter exercises the expired
// branch of processHint with a hint whose expire is in the past, and
// verifies the migration-source expired counter ticks.
func TestMigrationHint_ExpiredBumpsPerSourceCounter(t *testing.T) {
	t.Parallel()

	dm, _ := newMigrationHintTestDM(t)
	now := time.Now()
	item := &cache.Item{Key: "k1", Value: []byte("v"), Version: 1, Origin: migrationHintTestOrigin}

	migEntry := hintedEntry{
		item:     item,
		queuedAt: now.Add(-2 * time.Hour),
		expire:   now.Add(-time.Hour),
		source:   hintSourceMigration,
	}

	if action := dm.processHint(context.Background(), "peer-B", migEntry, now); action != 1 {
		t.Errorf("expired migration: want action=1 (remove), got %d", action)
	}

	if got := dm.metrics.hintedExpired.Load(); got != 1 {
		t.Errorf("aggregate hintedExpired: want 1, got %d", got)
	}

	if got := dm.metrics.migrationHintExpired.Load(); got != 1 {
		t.Errorf("migrationHintExpired: want 1, got %d", got)
	}
}

// TestMigrationHint_TransportErrorKeepsEntry pins the new
// retain-on-any-error contract: when a hint replay fails with any
// non-nil transport error (e.g. net.OpError, io.EOF, a scripted
// generic error like here), the hint stays queued for the next
// replay tick. Pre-fix this branch dropped the hint and bumped
// hintedDropped / migrationHintDropped, but that only matched the
// in-process ErrBackendNotFound sentinel — production HTTP/gRPC
// transports surfaced a different error class and lost the hint on
// the first replay attempt. TTL bounds total retry time, so a
// permanently-broken target still drains.
func TestMigrationHint_TransportErrorKeepsEntry(t *testing.T) {
	t.Parallel()

	dm, transport := newMigrationHintTestDM(t)
	transport.forwardSetErr.Store(errScriptedSimulate)

	now := time.Now()
	item := &cache.Item{Key: "k1", Value: []byte("v"), Version: 1, Origin: migrationHintTestOrigin}

	migEntry := hintedEntry{
		item:     item,
		queuedAt: now.Add(-time.Second),
		expire:   now.Add(time.Minute),
		source:   hintSourceMigration,
	}

	if action := dm.processHint(context.Background(), "peer-B", migEntry, now); action != 0 {
		t.Errorf("transport error: want action=0 (keep for retry), got %d", action)
	}

	if got := dm.metrics.hintedDropped.Load(); got != 0 {
		t.Errorf("aggregate hintedDropped on retainable error: want 0 (no longer bumped on replay failures), got %d", got)
	}

	if got := dm.metrics.migrationHintDropped.Load(); got != 0 {
		t.Errorf("migrationHintDropped on retainable error: want 0, got %d", got)
	}
}

// TestMigrationHint_NotFoundKeepsEntry confirms the not-found path
// (peer still absent — typically backend restarting) keeps the entry
// in the queue rather than dropping. Migration counters must NOT
// increment on this path; the hint will be retried on a later tick.
func TestMigrationHint_NotFoundKeepsEntry(t *testing.T) {
	t.Parallel()

	dm, transport := newMigrationHintTestDM(t)
	transport.forwardSetErr.Store(sentinel.ErrBackendNotFound)

	now := time.Now()
	item := &cache.Item{Key: "k1", Value: []byte("v"), Version: 1, Origin: migrationHintTestOrigin}

	migEntry := hintedEntry{
		item:     item,
		queuedAt: now.Add(-time.Second),
		expire:   now.Add(time.Minute),
		source:   hintSourceMigration,
	}

	if action := dm.processHint(context.Background(), "peer-B", migEntry, now); action != 0 {
		t.Errorf("not-found: want action=0 (keep), got %d", action)
	}

	if got := dm.metrics.migrationHintDropped.Load(); got != 0 {
		t.Errorf("migrationHintDropped on keep: want 0, got %d", got)
	}

	if got := dm.metrics.migrationHintReplayed.Load(); got != 0 {
		t.Errorf("migrationHintReplayed on keep: want 0, got %d", got)
	}
}

// TestMigrationHint_GlobalCapDropTagsSource confirms that even the
// global-cap rejection path (queue refused because hintMaxTotal /
// hintMaxBytes was already at the cap) routes through the
// migration-aware drop counter.
func TestMigrationHint_GlobalCapDropTagsSource(t *testing.T) {
	t.Parallel()

	dm, _ := newMigrationHintTestDM(t)

	// Force the cap so the next queue attempt trips the global drop branch.
	dm.hintMaxTotal = 1
	dm.hintTotal = 1

	item := &cache.Item{Key: "k1", Value: []byte("v"), Version: 1, Origin: migrationHintTestOrigin}
	dm.queueHint("peer-B", item, hintSourceMigration)

	if got := dm.metrics.hintedGlobalDropped.Load(); got != 1 {
		t.Errorf("aggregate hintedGlobalDropped: want 1, got %d", got)
	}

	if got := dm.metrics.migrationHintDropped.Load(); got != 1 {
		t.Errorf("migrationHintDropped on global-cap drop: want 1, got %d", got)
	}

	if got := dm.metrics.hintedQueued.Load(); got != 0 {
		t.Errorf("hintedQueued must not increment on drop: got %d", got)
	}
}
