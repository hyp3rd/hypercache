package backend

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"hash/fnv"
	"log/slog"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goccy/go-json"
	"github.com/hyp3rd/ewrap"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/hyp3rd/hypercache/internal/cluster"
	"github.com/hyp3rd/hypercache/internal/eventbus"
	"github.com/hyp3rd/hypercache/internal/sentinel"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// distTracerName is the OpenTelemetry instrumentation-library name for
// dist-backend spans. Stable, package-qualified — operators can grep
// span streams by it without depending on individual span names.
const distTracerName = "github.com/hyp3rd/hypercache/pkg/backend"

// Internal tuning constants.
const (
	defaultDistShardCount = 8  // default number of shards
	listPrealloc          = 32 // pre-allocation size for List results
)

// Merkle internal tuning constants & errors.
const (
	defaultMerkleChunkSize = 128
	merklePreallocEntries  = 256
	merkleVersionBytes     = 8
	shiftPerByte           = 8 // bit shift per byte when encoding uint64
)

var errNoTransport = ewrap.New("no transport")

// DistMemory is a sharded in-process distributed-like backend. It simulates
// distribution by consistent hashing across a fixed set of in-memory shards.
// It is intended for single-process multi-shard experimentation; NOT cross-process.
type DistMemory struct {
	shards     []*distShard
	shardCount int
	capacity   int // logical global capacity (0 = unlimited). Not strictly enforced yet.
	localNode  *cluster.Node
	membership *cluster.Membership
	ring       *cluster.Ring
	// eventBus broadcasts topology changes (`members`, `heartbeat`)
	// to in-process subscribers — primarily the management HTTP
	// server's SSE endpoint. Nil-safe: methods on *eventbus.Bus
	// panic on nil receiver, so we always construct one in
	// initMembershipIfNeeded even when the bus has no subscribers
	// yet (publish to a no-subscriber bus is a no-op).
	eventBus *eventbus.Bus
	// transport holds the active DistTransport behind an atomic.Pointer so that
	// callers (including the hint-replay goroutine) can read it without racing
	// against SetTransport / WithDistTransport / lazy HTTP server bring-up.
	// Use loadTransport() / storeTransport() instead of touching this directly.
	transport  atomic.Pointer[distTransportSlot]
	httpServer *distHTTPServer // optional internal HTTP server
	metrics    distMetrics
	// chaos (optional) wraps the configured transport with fault injection
	// for resilience testing. Set via WithDistChaos; nil in production.
	// storeTransport applies the wrapper transparently when chaos is set,
	// so the rest of the code path is unaware of the chaos surface.
	chaos *Chaos
	// repairQueue (optional) coalesces read-repair fan-out across the
	// read path. Set via WithDistReadRepairBatch with interval > 0;
	// nil means repairs dispatch synchronously inline (the historical
	// behavior). repairRemoteReplica checks this field on every call
	// and routes accordingly.
	repairQueue *repairQueue
	// repairBatchInterval + repairBatchSize hold the option values
	// captured at construction so the queue is built once with the
	// final config after all options have been applied. Zero
	// interval means batching is disabled.
	repairBatchInterval time.Duration
	repairBatchSize     int
	// configuration (static for now, future: dynamic membership/gossip)
	replication  int
	virtualNodes int
	nodeAddr     string
	nodeID       string
	seeds        []string // static seed node addresses

	// heartbeat / failure detection. Phase E added SWIM
	// self-refutation (refuteIfSuspected) and HTTP gossip
	// dissemination, retiring the prior "experimental" marker —
	// the path now disseminates suspect/dead transitions across
	// the cluster and lets a falsely-accused node bump its
	// incarnation to clear suspicion.
	hbInterval     time.Duration
	hbSuspectAfter time.Duration
	hbDeadAfter    time.Duration
	stopCh         chan struct{}

	// heartbeat sampling (Phase 2)
	hbSampleSize int // number of random peers to probe each tick (0=probe all)

	// indirectProbeK is the number of relay peers asked to probe a
	// target when the direct probe fails. SWIM-style filter for
	// caller-side network blips: the target is only marked suspect if
	// every relay also fails to reach it. 0 disables — direct probe
	// alone decides liveness, matching the pre-Phase-B behavior.
	indirectProbeK int

	// indirectProbeTimeout caps how long the per-relay probe call may
	// block. Defaults to half the heartbeat interval; tunable via
	// WithDistIndirectProbes for clusters with high inter-node RTT.
	indirectProbeTimeout time.Duration

	// consistency / versioning (initial)
	readConsistency  ConsistencyLevel
	writeConsistency ConsistencyLevel
	versionCounter   atomic.Uint64 // global monotonic for this node (lamport-like)

	// hinted handoff
	hintTTL        time.Duration
	hintReplayInt  time.Duration
	hintMaxPerNode int
	hintMaxTotal   int   // global cap on total queued hints (0 = unlimited)
	hintMaxBytes   int64 // approximate total bytes cap across all hints (0 = unlimited)
	hintsMu        sync.Mutex
	hints          map[string][]hintedEntry // nodeID -> queue
	hintTotal      int                      // current total hints queued (under hintsMu)
	hintBytes      int64                    // approximate bytes of queued hints (under hintsMu)
	hintStopCh     chan struct{}

	// parallel reads
	parallelReads bool

	// simple gossip
	gossipInterval time.Duration
	gossipStopCh   chan struct{}

	// anti-entropy
	merkleChunkSize int // number of keys per leaf chunk (power-of-two recommended)
	listKeysMax     int // cap for fallback ListKeys pulls (0 = unlimited)

	// periodic merkle auto-sync
	autoSyncInterval         time.Duration
	autoSyncStopCh           chan struct{}
	autoSyncPeersPerInterval int // limit number of peers synced per tick (0=all)

	lastAutoSyncDuration atomic.Int64 // nanoseconds of last full loop
	lastAutoSyncError    atomic.Value // error string or nil

	// adaptive backoff: when enabled (autoSyncMaxBackoffFactor > 1), the
	// auto-sync loop doubles its sleep after each tick that found zero
	// divergence across all peers, capped at autoSyncMaxBackoffFactor.
	// Any tick that pulls keys resets the factor to 1 immediately.
	autoSyncMaxBackoffFactor int          // 0/1 = disabled
	autoSyncBackoffFactor    atomic.Int64 // current multiplier; >=1 when adaptive enabled
	autoSyncCleanTicks       atomic.Int64 // cumulative clean ticks (whole-cycle, not per-peer)

	// tombstone version source when no prior item exists (monotonic per process)
	tombVersionCounter atomic.Uint64

	// tombstone retention / compaction
	tombstoneTTL      time.Duration
	tombstoneSweepInt time.Duration
	tombStopCh        chan struct{}

	// originalConfig retains the originating configuration (if constructed via config constructor)
	originalConfig any

	// latency histograms for core ops (Phase 1)
	latency *distLatencyCollector

	// rebalancing (Phase 3)
	rebalanceInterval      time.Duration
	rebalanceBatchSize     int
	rebalanceMaxConcurrent int
	rebalanceStopCh        chan struct{}
	lastRebalanceVersion   atomic.Uint64

	// httpLimits caps inbound request bodies (server) and inbound response
	// bodies (auto-created client) plus tunes timeouts and concurrency.
	// Zero-valued fields fall back to defaultDistHTTP* in
	// dist_http_server.go via DistHTTPLimits.withDefaults().
	httpLimits DistHTTPLimits

	// httpAuth configures bearer-token / signing-fn auth applied to both
	// the dist HTTP server (inbound validation) and the auto-created
	// HTTP client (outbound signing). Zero-value disables auth — same
	// behavior as before WithDistHTTPAuth was added.
	httpAuth DistHTTPAuth

	// lifeCtx is the server-lifetime context. Derived from the
	// constructor ctx but with its own cancel func wired into Stop, so
	// in-flight HTTP handlers and replica forwards observe Done() when
	// the user calls Stop — not just when the constructor ctx happens
	// to cancel (which it usually doesn't, since callers pass
	// context.Background()).
	lifeCtx    context.Context //nolint:containedctx // server-lifecycle, not request-scope
	lifeCancel context.CancelFunc

	// replica-only diff scan limits
	replicaDiffMaxPerTick int // 0 = unlimited

	// shedding / cleanup of keys we no longer own (grace period before delete to aid late reads / hinted handoff)
	removalGracePeriod time.Duration // if >0 we delay deleting keys we no longer own; after grace we remove locally

	// stopped guards Stop() against double-invocation (idempotent shutdown).
	stopped atomic.Bool

	// draining is set by Drain to mark this node for graceful shutdown:
	// /health returns 503, Set/Remove reject with sentinel.ErrDraining,
	// Get continues to serve. One-way — operators restart the process
	// to clear it. The CAS in Drain ensures the metric increment fires
	// exactly once per drain transition.
	draining atomic.Bool

	// tracer is the OpenTelemetry tracer used to create spans on the
	// public Get/Set/Remove ops and on replication fan-out. Defaults
	// to noop.NewTracerProvider so library code emits no spans unless
	// the caller opts in via WithDistTracerProvider. The noop tracer's
	// Span values are zero-allocation, so the always-on instrumentation
	// is cheap on the hot path when tracing is disabled.
	tracer trace.Tracer

	// meter is the OpenTelemetry meter used to register observable
	// counters/gauges that mirror the in-process distMetrics atomics.
	// Defaults to noop.NewMeterProvider so library code emits no
	// metrics unless the caller opts in via WithDistMeterProvider.
	// metricRegistration retains the registration handle returned by
	// meter.RegisterCallback so Stop can unregister cleanly — without
	// it the SDK would keep invoking the callback after shutdown.
	meter              metric.Meter
	metricRegistration metric.Registration

	// logger is the structured logger used by background loops
	// (heartbeat, hint replay, rebalance, gossip, merkle sync) and
	// error surfaces (transport bind failures, sync errors, dropped
	// hints). Defaults to a no-op handler writing to io.Discard so
	// library code does not write to stderr unless the caller opts
	// in via WithDistLogger. All log lines are pre-bound with
	// `node_id` so operators can grep/filter without the call sites
	// having to weave the ID through every record.
	logger *slog.Logger
}

const (
	defaultRebalanceBatchSize     = 128
	defaultRebalanceMaxConcurrent = 2
)

var errUnexpectedBackendType = ewrap.New("backend: unexpected backend type") // stable error (no dynamic wrapping needed)

// distTransportSlot wraps a DistTransport interface value so it can be stored
// in atomic.Pointer (atomic.Pointer requires a concrete pointer type).
type distTransportSlot struct{ t DistTransport }

// hintedEntry represents a deferred replica write.
type hintedEntry struct {
	item     *cache.Item
	queuedAt time.Time
	expire   time.Time
	size     int64      // approximate bytes for global cap accounting
	source   hintSource // why this hint exists (replication fan-out vs rebalance migration)
}

// hintSource records why a hint landed in the queue. Used to split
// the aggregate hint counters (which sum across both sources) into a
// migration-only view for operators tracking ownership-transfer
// liveness during rebalance phases. Replication-only counts are derivable
// as (aggregate - migration); we don't expose a third metric for it.
type hintSource int8

const (
	// hintSourceReplication marks hints produced by a failed write fan-out
	// to a peer replica. This is the historical default — every hint
	// landed in this bucket before migration-source tagging existed.
	hintSourceReplication hintSource = iota
	// hintSourceMigration marks hints produced by a rebalance tick that
	// could not deliver an item to its new primary owner.
	hintSourceMigration
)

// tombstone marks a delete intent with version ordering to prevent resurrection.
type tombstone struct {
	version uint64
	origin  string
	at      time.Time
}

// ConsistencyLevel defines read/write consistency semantics.
type ConsistencyLevel int

const (
	// ConsistencyOne returns after a single owner success (fast, may be stale).
	ConsistencyOne ConsistencyLevel = iota
	// ConsistencyQuorum waits for majority (floor(n/2)+1).
	ConsistencyQuorum
	// ConsistencyAll waits for all owners.
	ConsistencyAll
)

// String returns the human-readable form for logs and span attributes.
// Unknown values render as `consistency(<int>)` rather than panicking
// so a corrupted/forwards-compatible value still produces useful telemetry.
func (l ConsistencyLevel) String() string {
	switch l {
	case ConsistencyOne:
		return "ONE"
	case ConsistencyQuorum:
		return "QUORUM"
	case ConsistencyAll:
		return "ALL"
	default:
		return "consistency(" + strconv.Itoa(int(l)) + ")"
	}
}

// WithDistReadConsistency sets read consistency (default ONE).
func WithDistReadConsistency(l ConsistencyLevel) DistMemoryOption {
	return func(dm *DistMemory) { dm.readConsistency = l }
}

// WithDistWriteConsistency sets write consistency (default QUORUM).
func WithDistWriteConsistency(l ConsistencyLevel) DistMemoryOption {
	return func(dm *DistMemory) { dm.writeConsistency = l }
}

// WithDistTombstoneTTL configures how long tombstones are retained before subject to compaction (<=0 keeps indefinitely).
func WithDistTombstoneTTL(d time.Duration) DistMemoryOption {
	return func(dm *DistMemory) { dm.tombstoneTTL = d }
}

// WithDistTombstoneSweep sets sweep interval for tombstone compaction (<=0 disables automatic sweeps).
func WithDistTombstoneSweep(interval time.Duration) DistMemoryOption {
	return func(dm *DistMemory) { dm.tombstoneSweepInt = interval }
}

// Membership returns current membership reference (read-only usage).
func (dm *DistMemory) Membership() *cluster.Membership { return dm.membership }

// Ring returns the ring reference.
func (dm *DistMemory) Ring() *cluster.Ring { return dm.ring }

// LifecycleContext returns the server-lifecycle context derived from the
// ctx supplied to NewDistMemory. Stop cancels this context, so callers
// (including HTTP handlers and background loops) can observe shutdown
// without polling the various stopCh channels. Read-only — modifying
// the returned ctx has no effect.
func (dm *DistMemory) LifecycleContext() context.Context { return dm.lifeCtx }

type distShard struct {
	items             cache.ConcurrentMap
	tombs             map[string]tombstone        // per-key tombstones
	originalPrimary   map[string]cluster.NodeID   // recorded primary owner at first insert
	originalPrimaryMu sync.RWMutex                // guards originalPrimary
	originalOwners    map[string][]cluster.NodeID // recorded full owner set at first insert (for replica diff)
	originalOwnersMu  sync.RWMutex                // guards originalOwners
	removedAt         map[string]time.Time        // when we first observed we are no longer an owner (for grace shedding)
	removedAtMu       sync.Mutex                  // guards removedAt
}

// DistMemoryOption configures DistMemory backend.
type DistMemoryOption func(*DistMemory)

// WithDistShardCount sets number of shards (min 1).
func WithDistShardCount(n int) DistMemoryOption {
	return func(dm *DistMemory) {
		if n > 0 {
			dm.shardCount = n
		}
	}
}

// WithDistMerkleChunkSize sets the number of keys per leaf hash chunk (default 128 if 0).
func WithDistMerkleChunkSize(n int) DistMemoryOption {
	return func(dm *DistMemory) {
		if n > 0 {
			dm.merkleChunkSize = n
		}
	}
}

// WithDistMerkleAutoSync enables periodic anti-entropy sync attempts. If interval <= 0 disables.
func WithDistMerkleAutoSync(interval time.Duration) DistMemoryOption {
	return func(dm *DistMemory) {
		dm.autoSyncInterval = interval
	}
}

// WithDistMerkleAdaptiveBackoff enables adaptive scheduling for the Merkle
// anti-entropy loop. When maxFactor > 1, the loop doubles its sleep
// interval (1×, 2×, 4×, 8×, …) after each tick that found zero divergence
// across every peer, capped at maxFactor. Any tick with at least one
// dirty peer resets the factor to 1 on the next sleep — recovery is
// always immediate, never lazy.
//
// maxFactor <= 1 disables backoff (the default): the loop wakes at every
// `WithDistMerkleAutoSync` interval regardless of recent divergence.
//
// The current factor is exposed as `dist.auto_sync.backoff_factor` and
// the cumulative count of clean ticks as `dist.auto_sync.clean_ticks`.
// Each factor change is logged once at Info; no per-tick spam.
func WithDistMerkleAdaptiveBackoff(maxFactor int) DistMemoryOption {
	return func(dm *DistMemory) {
		if maxFactor < 1 {
			maxFactor = 0
		}

		dm.autoSyncMaxBackoffFactor = maxFactor
	}
}

// WithDistMerkleAutoSyncPeers limits number of peers synced per interval (0 or <0 = all).
func WithDistMerkleAutoSyncPeers(n int) DistMemoryOption {
	return func(dm *DistMemory) {
		dm.autoSyncPeersPerInterval = n
	}
}

// WithDistListKeysCap caps number of keys fetched via fallback ListKeys (0 = unlimited).
func WithDistListKeysCap(n int) DistMemoryOption {
	return func(dm *DistMemory) {
		if n >= 0 {
			dm.listKeysMax = n
		}
	}
}

// WithDistRebalanceInterval enables periodic ownership rebalancing checks (<=0 disables).
func WithDistRebalanceInterval(d time.Duration) DistMemoryOption {
	return func(dm *DistMemory) {
		dm.rebalanceInterval = d
	}
}

// WithDistRebalanceBatchSize sets max keys per transfer batch.
func WithDistRebalanceBatchSize(n int) DistMemoryOption {
	return func(dm *DistMemory) {
		if n > 0 {
			dm.rebalanceBatchSize = n
		}
	}
}

// WithDistRebalanceMaxConcurrent limits concurrent batch transfers.
func WithDistRebalanceMaxConcurrent(n int) DistMemoryOption {
	return func(dm *DistMemory) {
		if n > 0 {
			dm.rebalanceMaxConcurrent = n
		}
	}
}

// WithDistReplicaDiffMaxPerTick limits number of replica-diff replication operations performed per rebalance tick (0 = unlimited).
func WithDistReplicaDiffMaxPerTick(n int) DistMemoryOption {
	return func(dm *DistMemory) {
		if n > 0 {
			dm.replicaDiffMaxPerTick = n
		}
	}
}

// WithDistRemovalGrace sets grace period before shedding data for keys we no longer own (<=0 immediate remove disabled for now).
func WithDistRemovalGrace(d time.Duration) DistMemoryOption {
	return func(dm *DistMemory) {
		if d > 0 {
			dm.removalGracePeriod = d
		}
	}
}

// --- Merkle tree anti-entropy structures ---

// MerkleTree represents a binary hash tree over key/version pairs.
type MerkleTree struct { // minimal representation
	LeafHashes [][]byte // ordered leaf hashes
	Root       []byte
	ChunkSize  int
}

// BuildMerkleTree constructs a Merkle tree snapshot of local data (best-effort, locks each shard briefly).
func (dm *DistMemory) BuildMerkleTree() *MerkleTree {
	chunkSize := dm.merkleChunkSize
	if chunkSize <= 0 {
		chunkSize = defaultMerkleChunkSize
	}

	entries := dm.merkleEntries()
	if len(entries) == 0 {
		return &MerkleTree{ChunkSize: chunkSize}
	}

	slices.SortFunc(entries, func(a, b merkleKV) int {
		return strings.Compare(a.k, b.k)
	})

	hasher := sha256.New()
	buf := make([]byte, merkleVersionBytes)
	leaves := make([][]byte, 0, (len(entries)+chunkSize-1)/chunkSize)

	for i := 0; i < len(entries); i += chunkSize {
		end := min(i+chunkSize, len(entries))

		hasher.Reset()

		for _, e := range entries[i:end] {
			_, _ = hasher.Write([]byte(e.k))
			encodeUint64BigEndian(buf, e.v)

			_, _ = hasher.Write(buf)
		}

		leaves = append(leaves, append([]byte(nil), hasher.Sum(nil)...))
	}

	root := foldMerkle(leaves, hasher)

	return &MerkleTree{LeafHashes: leaves, Root: append([]byte(nil), root...), ChunkSize: chunkSize}
}

// merkleKV is an internal pair used during tree construction & sync.
type merkleKV struct {
	k string
	v uint64
}

// DiffLeafRanges compares two trees and returns indexes of differing leaf chunks.
func (mt *MerkleTree) DiffLeafRanges(other *MerkleTree) []int {
	if mt == nil || other == nil {
		return nil
	}

	if len(mt.LeafHashes) != len(other.LeafHashes) { // size mismatch -> full resync
		idxs := make([]int, len(mt.LeafHashes))
		for i := range idxs {
			idxs[i] = i
		}

		return idxs
	}

	var diffs []int

	for i := range mt.LeafHashes {
		if !equalBytes(mt.LeafHashes[i], other.LeafHashes[i]) {
			diffs = append(diffs, i)
		}
	}

	return diffs
}

func equalBytes(a, b []byte) bool { // tiny helper
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

// SyncWith performs Merkle anti-entropy against a remote node (pull newer versions for differing chunks).
//
// Returns nil even when the local and remote trees agree (a "clean" sync).
// Callers that need to distinguish clean from dirty cycles — currently only
// the adaptive auto-sync backoff path — use syncWithStatus directly.
func (dm *DistMemory) SyncWith(ctx context.Context, nodeID string) error {
	_, err := dm.syncWithStatus(ctx, nodeID)

	return err
}

// WithDistCapacity sets logical capacity (not strictly enforced yet).
func WithDistCapacity(capacity int) DistMemoryOption {
	return func(dm *DistMemory) { dm.capacity = capacity }
}

// WithDistMembership injects an existing membership and (optionally) a local node for multi-node tests.
// If node is nil a new one will be created.
func WithDistMembership(m *cluster.Membership, node *cluster.Node) DistMemoryOption {
	return func(dm *DistMemory) {
		if m != nil {
			dm.membership = m
			dm.localNode = node
		}
	}
}

// WithDistTransport sets a transport used for forwarding / replication.
func WithDistTransport(t DistTransport) DistMemoryOption {
	return func(dm *DistMemory) { dm.storeTransport(t) }
}

// WithDistHeartbeatSample sets how many random peers to probe per heartbeat tick (0=all).
func WithDistHeartbeatSample(k int) DistMemoryOption {
	return func(dm *DistMemory) { dm.hbSampleSize = k }
}

// SetTransport sets the transport post-construction (testing helper).
func (dm *DistMemory) SetTransport(t DistTransport) { dm.storeTransport(t) }

// WithDistHeartbeat configures heartbeat interval and suspect/dead thresholds.
// If interval <= 0 heartbeat is disabled.
func WithDistHeartbeat(interval, suspectAfter, deadAfter time.Duration) DistMemoryOption {
	return func(dm *DistMemory) {
		dm.hbInterval = interval
		dm.hbSuspectAfter = suspectAfter
		dm.hbDeadAfter = deadAfter
	}
}

// WithDistIndirectProbes enables SWIM-style indirect probing for the
// heartbeat path. When a direct probe to a peer fails, this node asks
// `k` random alive peers to probe the target on its behalf; the target
// is only marked suspect if every relay also fails. Filters
// caller-side network blips (NIC reset, brief upstream outage, single
// stuck connection in a pool) that would otherwise cause spurious
// suspect/dead transitions.
//
// `timeout` caps each relay's probe call. Pass 0 to inherit the
// default (half the configured heartbeat interval).
//
// k = 0 disables indirect probing — direct probe alone decides
// liveness, matching the pre-Phase-B behavior. Recommended k = 3 for
// production clusters; clusters with fewer than k+1 alive peers scale
// down automatically (probe whatever's available).
func WithDistIndirectProbes(k int, timeout time.Duration) DistMemoryOption {
	return func(dm *DistMemory) {
		if k < 0 {
			k = 0
		}

		dm.indirectProbeK = k
		dm.indirectProbeTimeout = timeout
	}
}

// WithDistReplication sets ring replication factor (owners per key).
func WithDistReplication(n int) DistMemoryOption {
	return func(dm *DistMemory) {
		if n > 0 {
			dm.replication = n
		}
	}
}

// WithDistVirtualNodes sets number of virtual nodes per physical node for consistent hash ring.
func WithDistVirtualNodes(n int) DistMemoryOption {
	return func(dm *DistMemory) {
		if n > 0 {
			dm.virtualNodes = n
		}
	}
}

// WithDistHintTTL sets TTL for hinted handoff entries.
func WithDistHintTTL(d time.Duration) DistMemoryOption {
	return func(dm *DistMemory) { dm.hintTTL = d }
}

// WithDistHintReplayInterval sets how often to attempt replay of hints.
func WithDistHintReplayInterval(d time.Duration) DistMemoryOption {
	return func(dm *DistMemory) { dm.hintReplayInt = d }
}

// WithDistHintMaxPerNode caps number of queued hints per target node.
func WithDistHintMaxPerNode(n int) DistMemoryOption {
	return func(dm *DistMemory) {
		if n > 0 {
			dm.hintMaxPerNode = n
		}
	}
}

// WithDistHintMaxTotal sets a global cap on total queued hints across all nodes.
func WithDistHintMaxTotal(n int) DistMemoryOption {
	return func(dm *DistMemory) {
		if n > 0 {
			dm.hintMaxTotal = n
		}
	}
}

// WithDistHintMaxBytes sets an approximate byte cap for all queued hints.
func WithDistHintMaxBytes(b int64) DistMemoryOption {
	return func(dm *DistMemory) {
		if b > 0 {
			dm.hintMaxBytes = b
		}
	}
}

// WithDistParallelReads enables parallel quorum/all read fan-out.
func WithDistParallelReads(enable bool) DistMemoryOption {
	return func(dm *DistMemory) { dm.parallelReads = enable }
}

// WithDistGossipInterval enables simple membership gossip at provided interval.
func WithDistGossipInterval(d time.Duration) DistMemoryOption {
	return func(dm *DistMemory) { dm.gossipInterval = d }
}

// WithDistNode identity (id optional; derived from address if empty). Address used for future RPC.
func WithDistNode(id, address string) DistMemoryOption {
	return func(dm *DistMemory) {
		if address != "" {
			dm.nodeAddr = address
		}

		dm.nodeID = id
	}
}

// WithDistSeeds configures static seed node addresses.
func WithDistSeeds(addresses []string) DistMemoryOption {
	cp := make([]string, 0, len(addresses))
	for _, a := range addresses {
		if a != "" {
			cp = append(cp, a)
		}
	}

	return func(dm *DistMemory) { dm.seeds = cp }
}

// WithDistHTTPLimits configures the HTTP transport limits for the dist
// HTTP server (inbound request bodies, timeouts, concurrency) and the
// auto-created HTTP client (response body cap, request timeout). Partial
// overrides are honored: zero-valued fields inherit the package defaults
// from DistHTTPLimits.withDefaults.
//
// This option only affects the *internal* HTTP server/client created by
// tryStartHTTP — explicitly-supplied transports via WithDistTransport are
// the caller's responsibility to bound.
func WithDistHTTPLimits(limits DistHTTPLimits) DistMemoryOption {
	return func(dm *DistMemory) { dm.httpLimits = limits }
}

// WithDistHTTPAuth configures bearer-token (or custom verify/sign)
// authentication for the dist HTTP server and auto-created HTTP client.
// See DistHTTPAuth for the policy struct shape and defaults.
//
// Operators must apply the same auth policy to every node in the
// cluster — peers with mismatched tokens will reject each other's
// requests with HTTP 401. Like WithDistHTTPLimits this only affects the
// internal transport; an externally-supplied DistTransport is the
// caller's responsibility to authenticate.
//
// NewDistMemory validates the resulting policy and returns
// sentinel.ErrInsecureAuthConfig if ClientSign is set without a
// matching inbound verifier (Token or ServerVerify) and
// AllowAnonymousInbound is not set — see DistHTTPAuth for the rationale.
func WithDistHTTPAuth(auth DistHTTPAuth) DistMemoryOption {
	return func(dm *DistMemory) { dm.httpAuth = auth }
}

// WithDistLogger supplies a structured logger for the dist backend's
// background loops (heartbeat, hint replay, rebalance, gossip, merkle
// auto-sync) and operational error surfaces (HTTP listener failures,
// transport errors, dropped hints). The supplied logger is wrapped with
// `node_id` and `component=dist_memory` attributes before use, so call
// sites do not need to weave the node ID through every record.
//
// Pass slog.Default() to inherit the application's logger, or supply a
// custom *slog.Logger with the desired level / handler. Zero-value (no
// option call) keeps the dist backend silent — the default uses an
// io.Discard handler, which means library code never writes to stderr
// unless the caller opts in.
//
// nil is treated as "no change" — useful when callers conditionally
// build options.
func WithDistLogger(logger *slog.Logger) DistMemoryOption {
	return func(dm *DistMemory) {
		if logger != nil {
			dm.logger = logger
		}
	}
}

// WithDistReadRepairBatch enables async coalescing of read-repair
// fan-out. When interval > 0, repairs from the read path are
// queued by destination peer; the queue flushes periodically OR
// when a peer's pending count hits maxBatchSize. Repairs to the
// same (peer, key) collapse to the highest-version entry —
// concurrent reads of the same hot key produce one repair, not N.
//
// Default (interval = 0 or maxBatchSize <= 0): repairs dispatch
// synchronously inside the Get path. Existing callers asserting
// "replica healed by the time Get returns" see byte-identical
// behavior.
//
// Trade-off: batched mode introduces a window (up to `interval`)
// where a divergent replica stays divergent. Merkle anti-entropy
// is the convergence safety net; the read-repair path is and
// always was best-effort. Stop() drains pending entries before
// returning so a clean shutdown doesn't lose queued repairs.
func WithDistReadRepairBatch(interval time.Duration, maxBatchSize int) DistMemoryOption {
	return func(dm *DistMemory) {
		if interval <= 0 || maxBatchSize <= 0 {
			// Out-of-range values disable batching rather than
			// silently coercing to a tiny non-zero value the caller
			// didn't intend.
			dm.repairBatchInterval = 0
			dm.repairBatchSize = 0

			return
		}

		dm.repairBatchInterval = interval
		dm.repairBatchSize = maxBatchSize
	}
}

// WithDistTracerProvider supplies an OpenTelemetry TracerProvider for
// the dist backend. When set, every public Get/Set/Remove call opens a
// span (`dist.get` / `dist.set` / `dist.remove`) carrying consistency
// level and key length attributes; replication fan-out adds child spans
// (`dist.replicate.set` / `dist.replicate.remove`) per peer so operators
// can see where time is spent under load.
//
// Span attributes intentionally omit the cache key value — keys can be
// PII (user IDs, session tokens). Only `cache.key.length` is recorded.
// Callers needing the key value should add their own outer span before
// invoking the dist backend.
//
// Pass otel.GetTracerProvider() to inherit the application's globally
// registered provider, or supply a custom *sdktrace.TracerProvider to
// route dist spans to a dedicated exporter. nil is treated as "no
// change" — useful for conditional option building.
//
// Library default (no option call) installs a no-op tracer, so library
// code emits no spans unless the caller opts in.
func WithDistTracerProvider(tp trace.TracerProvider) DistMemoryOption {
	return func(dm *DistMemory) {
		if tp != nil {
			dm.tracer = tp.Tracer(distTracerName)
		}
	}
}

// WithDistMeterProvider supplies an OpenTelemetry MeterProvider for the
// dist backend. When set, NewDistMemory registers an observable
// instrument for every field on DistMetrics — counters for cumulative
// totals (writes, forwards, hints, rebalance batches, etc.), gauges for
// current state (active tombstones, hint queue size, alive/suspect/dead
// member counts) and last-operation latencies (merkle build/diff/fetch
// nanoseconds, last rebalance/auto-sync duration). Instrument names use
// the `dist.` prefix so a Prometheus exporter can route them under a
// dedicated subsystem.
//
// A single registered callback drives all instruments: on each
// collection cycle it takes one Metrics() snapshot and observes every
// instrument from that snapshot. There is no per-operation overhead
// when a real meter is configured beyond the existing atomic counters
// the dist backend already maintains.
//
// Pass otel.GetMeterProvider() to inherit the application's globally
// registered provider, or supply a custom MeterProvider built via the
// otel/sdk/metric package (typically wrapping a Prometheus exporter or
// OTLP pipeline). nil is treated as "no change" — useful for
// conditional option building.
//
// Library default (no option call) installs a no-op meter, so library
// code emits no metrics unless the caller opts in.
func WithDistMeterProvider(mp metric.MeterProvider) DistMemoryOption {
	return func(dm *DistMemory) {
		if mp != nil {
			dm.meter = mp.Meter(distTracerName)
		}
	}
}

// NewDistMemory creates a new DistMemory backend.
func NewDistMemory(ctx context.Context, opts ...DistMemoryOption) (IBackend[DistMemory], error) {
	// Derive a server-lifetime context from the caller's ctx so that:
	//   1. If the caller cancels their ctx, our background work and HTTP
	//      handlers see it (chains via WithCancel parent).
	//   2. Stop() can independently cancel without touching the caller's
	//      ctx — gives operators a deterministic shutdown signal even
	//      when they pass context.Background().
	lifeCtx, lifeCancel := context.WithCancel(ctx)

	dm := &DistMemory{
		shardCount:       defaultDistShardCount,
		replication:      1,
		readConsistency:  ConsistencyOne,
		writeConsistency: ConsistencyQuorum,
		latency:          newDistLatencyCollector(),
		lifeCtx:          lifeCtx,
		lifeCancel:       lifeCancel,
	}
	for _, opt := range opts {
		opt(dm)
	}

	// Reject incoherent auth configs (e.g. ClientSign-only) before
	// any subsystem captures the policy. validate returns
	// sentinel.ErrInsecureAuthConfig for the misconfiguration that
	// previously caused silent inbound bypass.
	authErr := dm.httpAuth.validate()
	if authErr != nil {
		lifeCancel()

		return nil, authErr
	}

	dm.installTelemetryDefaults()

	dm.ensureShardConfig()
	dm.initMembershipIfNeeded()
	// Pass the lifecycle ctx to subsystems that capture it (HTTP handlers,
	// background loops). The constructor ctx is used only for operations
	// that must complete during NewDistMemory itself (e.g. listener bind).
	dm.tryStartHTTP(ctx)
	dm.logClusterJoin()

	dm.startHeartbeatIfEnabled(lifeCtx)
	dm.startHintReplayIfEnabled(lifeCtx)
	dm.startGossipIfEnabled()
	dm.startAutoSyncIfEnabled(lifeCtx)
	dm.startTombstoneSweeper()
	dm.startRebalancerIfEnabled(lifeCtx)
	dm.startRepairQueueIfEnabled(lifeCtx)

	return dm, nil
}

// NewDistMemoryWithConfig builds a DistMemory from an external dist.Config shape without introducing a direct import here.
// Accepts a generic 'cfg' to avoid adding a dependency layer; expects exported fields matching internal/dist Config.
func NewDistMemoryWithConfig(ctx context.Context, cfg any, opts ...DistMemoryOption) (IBackend[DistMemory], error) {
	type minimalConfig struct { // external mirror subset
		NodeID           string
		BindAddr         string
		AdvertiseAddr    string
		Seeds            []string
		Replication      int
		VirtualNodes     int
		ReadConsistency  int
		WriteConsistency int
		HintTTL          time.Duration
		HintReplay       time.Duration
		HintMaxPerNode   int
	}

	var mc minimalConfig

	if asserted, ok := cfg.(minimalConfig); ok { // best-effort copy
		mc = asserted
	}

	derived := distOptionsFromMinimal(mc)

	all := make([]DistMemoryOption, 0, len(derived)+len(opts))

	all = append(all, derived...)
	all = append(all, opts...)

	dmIface, err := NewDistMemory(ctx, all...)
	if err != nil {
		return nil, err
	}

	dm, ok := dmIface.(*DistMemory)
	if !ok {
		return nil, errUnexpectedBackendType
	}

	dm.originalConfig = cfg

	return dm, nil
}

// distOptionsFromMinimal converts a minimalConfig into DistMemoryOptions (pure helper for lint complexity reduction).
func distOptionsFromMinimal(mc struct {
	NodeID, BindAddr, AdvertiseAddr   string
	Seeds                             []string
	Replication, VirtualNodes         int
	ReadConsistency, WriteConsistency int
	HintTTL, HintReplay               time.Duration
	HintMaxPerNode                    int
},
) []DistMemoryOption {
	var opts []DistMemoryOption

	add := func(cond bool, opt DistMemoryOption) { // helper reduces complexity in parent
		if cond {
			opts = append(opts, opt)
		}
	}

	add(mc.NodeID != "" || mc.AdvertiseAddr != "", WithDistNode(mc.NodeID, mc.AdvertiseAddr))

	if len(mc.Seeds) > 0 { // seeds need copy; keep single conditional here
		cp := make([]string, len(mc.Seeds))
		copy(cp, mc.Seeds)

		opts = append(opts, WithDistSeeds(cp))
	}

	add(mc.Replication > 0, WithDistReplication(mc.Replication))
	add(mc.VirtualNodes > 0, WithDistVirtualNodes(mc.VirtualNodes))
	add(
		mc.ReadConsistency >= 0 && mc.ReadConsistency <= int(ConsistencyQuorum),
		WithDistReadConsistency(ConsistencyLevel(mc.ReadConsistency)),
	)
	add(
		mc.WriteConsistency >= 0 && mc.WriteConsistency <= int(ConsistencyQuorum),
		WithDistWriteConsistency(ConsistencyLevel(mc.WriteConsistency)),
	)
	add(mc.HintTTL > 0, WithDistHintTTL(mc.HintTTL))
	add(mc.HintReplay > 0, WithDistHintReplayInterval(mc.HintReplay))
	add(mc.HintMaxPerNode > 0, WithDistHintMaxPerNode(mc.HintMaxPerNode))

	return opts
}

// ensureShardConfig initializes shards respecting configured shardCount.
// helper methods relocated after exported methods for lint ordering.

// Capacity returns logical capacity.
func (dm *DistMemory) Capacity() int { return dm.capacity }

// SetCapacity sets logical capacity.
func (dm *DistMemory) SetCapacity(capacity int) {
	if capacity >= 0 {
		dm.capacity = capacity
	}
}

// Count returns total items across shards.
func (dm *DistMemory) Count(_ context.Context) int {
	total := 0

	for _, s := range dm.shards {
		total += s.items.Count()
	}

	return total
}

// Get fetches item.
func (dm *DistMemory) Get(ctx context.Context, key string) (*cache.Item, bool) {
	ctx, span := dm.tracer.Start(
		ctx, "dist.get",
		trace.WithAttributes(
			attribute.Int("cache.key.length", len(key)),
			attribute.String("dist.consistency", dm.readConsistency.String()),
		),
	)
	defer span.End()

	start := time.Now()
	item, hit := dm.getImpl(ctx, key)

	span.SetAttributes(attribute.Bool("cache.hit", hit))

	if dm.latency != nil {
		dm.latency.observe(opGet, time.Since(start))
	}

	return item, hit
}

// Set stores item.
func (dm *DistMemory) Set(ctx context.Context, item *cache.Item) error {
	validateErr := item.Valid()
	if validateErr != nil {
		return validateErr
	}

	ctx, span := dm.tracer.Start(
		ctx, "dist.set",
		trace.WithAttributes(
			attribute.Int("cache.key.length", len(item.Key)),
			attribute.String("dist.consistency", dm.writeConsistency.String()),
		),
	)
	defer span.End()

	start := time.Now()

	err := dm.setImpl(ctx, item, span)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}

	if dm.latency != nil {
		dm.latency.observe(opSet, time.Since(start))
	}

	return err
}

// --- Consistency helper methods. ---

// List aggregates items (no ordering, then filters applied per interface contract not yet integrated; kept simple);.
func (dm *DistMemory) List(_ context.Context, _ ...IFilter) ([]*cache.Item, error) {
	items := make([]*cache.Item, 0, listPrealloc)
	for _, s := range dm.shards {
		for _, it := range s.items.All() {
			cloned := *it

			items = append(items, &cloned)
		}
	}

	return items, nil
}

// Remove deletes keys.
func (dm *DistMemory) Remove(ctx context.Context, keys ...string) error {
	ctx, span := dm.tracer.Start(
		ctx, "dist.remove",
		trace.WithAttributes(attribute.Int("dist.keys.count", len(keys))),
	)
	defer span.End()

	start := time.Now()

	err := dm.removeImpl(ctx, keys)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}

	if dm.latency != nil {
		dm.latency.observe(opRemove, time.Since(start))
	}

	return err
}

// Clear wipes all shards.
func (dm *DistMemory) Clear(ctx context.Context) error {
	done := make(chan struct{})

	go func() {
		for _, s := range dm.shards {
			s.items.Clear()
		}

		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return sentinel.ErrTimeoutOrCanceled
	}
}

// LocalContains returns true if key exists in local shard (ignores ownership).
func (dm *DistMemory) LocalContains(key string) bool {
	_, ok := dm.shardFor(key).items.Get(key)

	return ok
}

// Touch updates the last access time and access count for a key.
func (dm *DistMemory) Touch(_ context.Context, key string) bool {
	return dm.shardFor(key).items.Touch(key)
}

// DebugDropLocal removes a key only from the local shard (for tests / read-repair validation).
func (dm *DistMemory) DebugDropLocal(key string) { dm.shardFor(key).items.Remove(key) }

// DebugInject stores an item directly into the local shard (no replication / ownership checks) for tests.
func (dm *DistMemory) DebugInject(it *cache.Item) {
	if it == nil {
		return
	}

	sh := dm.shardFor(it.Key)
	// test helper: injecting a concrete item implies intent to resurrect; clear any tombstone so normal version comparison applies
	delete(sh.tombs, it.Key)
	sh.items.Set(it.Key, it)
}

// LocalNodeID returns this instance's node ID (testing helper).
func (dm *DistMemory) LocalNodeID() cluster.NodeID { return dm.localNode.ID }

// LocalNodeAddr returns the configured node address (host:port) used by HTTP server.
func (dm *DistMemory) LocalNodeAddr() string {
	return dm.nodeAddr
}

// SetLocalNode manually sets the local node (testing helper before starting HTTP).
func (dm *DistMemory) SetLocalNode(node *cluster.Node) {
	dm.localNode = node
	if dm.nodeAddr == "" && node != nil {
		dm.nodeAddr = node.Address
	}

	if dm.membership != nil && node != nil {
		dm.membership.Upsert(node)
	}
}

// DebugOwners returns current owners slice for a key (for tests).
func (dm *DistMemory) DebugOwners(key string) []cluster.NodeID {
	if dm.ring == nil {
		return nil
	}

	return dm.ring.Lookup(key)
}

// Transport interfaces & in-process implementation are defined in dist_transport.go

// distMetrics holds internal counters (best-effort, not atomic snapshot consistent).
type distMetrics struct {
	forwardGet                   atomic.Int64
	forwardSet                   atomic.Int64
	forwardRemove                atomic.Int64
	replicaFanoutSet             atomic.Int64
	replicaFanoutRemove          atomic.Int64
	readRepair                   atomic.Int64
	readRepairBatched            atomic.Int64 // repairs dispatched via the batched flush path
	readRepairCoalesced          atomic.Int64 // repairs short-circuited because a queued entry already covered the (peer, key)
	replicaGetMiss               atomic.Int64
	heartbeatSuccess             atomic.Int64
	heartbeatFailure             atomic.Int64
	indirectProbeSuccess         atomic.Int64 // indirect probes that succeeded (target reachable via relay)
	indirectProbeFailure         atomic.Int64 // indirect probes that failed (relay confirmed target unreachable)
	indirectProbeRefuted         atomic.Int64 // direct probe failed but indirect probe succeeded — target reachable, caller's network was the issue
	drains                       atomic.Int64 // number of drain transitions observed on this node (one-way, so 0 or 1 in normal use)
	nodesSuspect                 atomic.Int64 // number of times a node transitioned to suspect
	nodesDead                    atomic.Int64 // number of times a node transitioned to dead/pruned
	nodesRemoved                 atomic.Int64
	versionConflicts             atomic.Int64 // times a newer version (or tie-broken origin) replaced previous candidate
	versionTieBreaks             atomic.Int64 // subset of conflicts decided by origin tie-break
	readPrimaryPromote           atomic.Int64 // times read path skipped unreachable primary and promoted next owner
	hintedQueued                 atomic.Int64 // hints queued (both sources)
	hintedReplayed               atomic.Int64 // hints successfully replayed
	hintedExpired                atomic.Int64 // hints expired before delivery
	hintedDropped                atomic.Int64 // hints dropped due to non-not-found transport errors
	hintedGlobalDropped          atomic.Int64 // hints dropped due to global caps (count/bytes)
	hintedBytes                  atomic.Int64 // approximate total bytes currently queued (best-effort)
	migrationHintQueued          atomic.Int64 // subset of hintedQueued: rebalance migration source only
	migrationHintReplayed        atomic.Int64 // subset of hintedReplayed: migration-source hints that drained
	migrationHintExpired         atomic.Int64 // subset of hintedExpired: migration-source hints aged out
	migrationHintDropped         atomic.Int64 // subset of hintedDropped + hintedGlobalDropped: migration-source hints that died
	migrationHintLastAgeNanos    atomic.Int64 // queue residency of the most-recently-replayed migration hint (ns)
	merkleSyncs                  atomic.Int64 // merkle sync operations completed
	merkleKeysPulled             atomic.Int64 // keys applied during sync
	merkleBuildNanos             atomic.Int64 // last build duration (ns)
	merkleDiffNanos              atomic.Int64 // last diff duration (ns)
	merkleFetchNanos             atomic.Int64 // last remote fetch duration (ns)
	autoSyncLoops                atomic.Int64 // number of auto-sync ticks executed
	tombstonesActive             atomic.Int64 // approximate active tombstones
	tombstonesPurged             atomic.Int64 // cumulative purged tombstones
	writeQuorumFailures          atomic.Int64 // number of write operations that failed quorum
	writeAcks                    atomic.Int64 // cumulative replica write acks (includes primary)
	writeAttempts                atomic.Int64 // total write operations attempted (Set)
	rebalancedKeys               atomic.Int64 // keys migrated during rebalancing
	rebalanceBatches             atomic.Int64 // number of batches processed
	rebalanceThrottle            atomic.Int64 // times rebalance was throttled due to concurrency limits
	rebalanceLastNanos           atomic.Int64 // duration of last full rebalance scan (ns)
	rebalanceReplicaDiff         atomic.Int64 // number of keys whose value was pushed to newly added replicas (replica-only diff)
	rebalanceReplicaDiffThrottle atomic.Int64 // number of times replica diff scan exited early due to per-tick limit
	rebalancedPrimary            atomic.Int64 // number of keys whose primary ownership changed (migrations to new primary)
}

// DistMetrics snapshot.
type DistMetrics struct {
	ForwardGet                   int64
	ForwardSet                   int64
	ForwardRemove                int64
	ReplicaFanoutSet             int64
	ReplicaFanoutRemove          int64
	ReadRepair                   int64
	ReadRepairBatched            int64 // subset of ReadRepair dispatched via the async coalescer
	ReadRepairCoalesced          int64 // repairs short-circuited by the coalescer (duplicate same-version entries)
	ReplicaGetMiss               int64
	HeartbeatSuccess             int64
	HeartbeatFailure             int64
	IndirectProbeSuccess         int64
	IndirectProbeFailure         int64
	IndirectProbeRefuted         int64
	Drains                       int64
	NodesSuspect                 int64
	NodesDead                    int64
	NodesRemoved                 int64
	VersionConflicts             int64
	VersionTieBreaks             int64
	ReadPrimaryPromote           int64
	HintedQueued                 int64
	HintedReplayed               int64
	HintedExpired                int64
	HintedDropped                int64
	HintedGlobalDropped          int64
	HintedBytes                  int64
	MigrationHintQueued          int64 // subset of HintedQueued attributable to rebalance migrations
	MigrationHintReplayed        int64 // subset of HintedReplayed for migration hints
	MigrationHintExpired         int64 // subset of HintedExpired for migration hints
	MigrationHintDropped         int64 // subset of HintedDropped + HintedGlobalDropped for migration hints
	MigrationHintLastAgeNanos    int64 // queue residency of the most-recently-replayed migration hint (ns)
	MerkleSyncs                  int64
	MerkleKeysPulled             int64
	MerkleBuildNanos             int64
	MerkleDiffNanos              int64
	MerkleFetchNanos             int64
	AutoSyncLoops                int64
	LastAutoSyncNanos            int64
	LastAutoSyncError            string
	AutoSyncCleanTicks           int64 // cumulative ticks where every peer returned no divergence
	AutoSyncBackoffFactor        int64 // current adaptive-backoff multiplier (1 when disabled or freshly reset)
	ChaosDrops                   int64 // transport calls dropped by configured Chaos (test-only; zero in prod)
	ChaosLatencies               int64 // transport calls that had latency injected by Chaos (test-only)
	TombstonesActive             int64
	TombstonesPurged             int64
	WriteQuorumFailures          int64
	WriteAcks                    int64
	WriteAttempts                int64
	RebalancedKeys               int64
	RebalanceBatches             int64
	RebalanceThrottle            int64
	RebalanceLastNanos           int64
	RebalancedReplicaDiff        int64
	RebalanceReplicaDiffThrottle int64
	RebalancedPrimary            int64
	MembershipVersion            uint64 // current membership version (incremented on changes)
	MembersAlive                 int64  // current alive members
	MembersSuspect               int64  // current suspect members
	MembersDead                  int64  // current dead members
}

// Metrics returns a snapshot of distributed metrics.
// Metrics returns a snapshot of distributed metrics.
func (dm *DistMemory) Metrics() DistMetrics {
	lastErr := ""
	if v := dm.lastAutoSyncError.Load(); v != nil {
		if s, ok := v.(string); ok {
			lastErr = s
		}
	}

	memSnap := dm.membershipSnapshot()

	return DistMetrics{
		ForwardGet:                   dm.metrics.forwardGet.Load(),
		ForwardSet:                   dm.metrics.forwardSet.Load(),
		ForwardRemove:                dm.metrics.forwardRemove.Load(),
		ReplicaFanoutSet:             dm.metrics.replicaFanoutSet.Load(),
		ReplicaFanoutRemove:          dm.metrics.replicaFanoutRemove.Load(),
		ReadRepair:                   dm.metrics.readRepair.Load(),
		ReadRepairBatched:            dm.metrics.readRepairBatched.Load(),
		ReadRepairCoalesced:          dm.metrics.readRepairCoalesced.Load(),
		ReplicaGetMiss:               dm.metrics.replicaGetMiss.Load(),
		HeartbeatSuccess:             dm.metrics.heartbeatSuccess.Load(),
		HeartbeatFailure:             dm.metrics.heartbeatFailure.Load(),
		IndirectProbeSuccess:         dm.metrics.indirectProbeSuccess.Load(),
		IndirectProbeFailure:         dm.metrics.indirectProbeFailure.Load(),
		IndirectProbeRefuted:         dm.metrics.indirectProbeRefuted.Load(),
		Drains:                       dm.metrics.drains.Load(),
		NodesSuspect:                 dm.metrics.nodesSuspect.Load(),
		NodesDead:                    dm.metrics.nodesDead.Load(),
		NodesRemoved:                 dm.metrics.nodesRemoved.Load(),
		VersionConflicts:             dm.metrics.versionConflicts.Load(),
		VersionTieBreaks:             dm.metrics.versionTieBreaks.Load(),
		ReadPrimaryPromote:           dm.metrics.readPrimaryPromote.Load(),
		HintedQueued:                 dm.metrics.hintedQueued.Load(),
		HintedReplayed:               dm.metrics.hintedReplayed.Load(),
		HintedExpired:                dm.metrics.hintedExpired.Load(),
		HintedDropped:                dm.metrics.hintedDropped.Load(),
		HintedGlobalDropped:          dm.metrics.hintedGlobalDropped.Load(),
		HintedBytes:                  dm.metrics.hintedBytes.Load(),
		MigrationHintQueued:          dm.metrics.migrationHintQueued.Load(),
		MigrationHintReplayed:        dm.metrics.migrationHintReplayed.Load(),
		MigrationHintExpired:         dm.metrics.migrationHintExpired.Load(),
		MigrationHintDropped:         dm.metrics.migrationHintDropped.Load(),
		MigrationHintLastAgeNanos:    dm.metrics.migrationHintLastAgeNanos.Load(),
		MerkleSyncs:                  dm.metrics.merkleSyncs.Load(),
		MerkleKeysPulled:             dm.metrics.merkleKeysPulled.Load(),
		MerkleBuildNanos:             dm.metrics.merkleBuildNanos.Load(),
		MerkleDiffNanos:              dm.metrics.merkleDiffNanos.Load(),
		MerkleFetchNanos:             dm.metrics.merkleFetchNanos.Load(),
		AutoSyncLoops:                dm.metrics.autoSyncLoops.Load(),
		LastAutoSyncNanos:            dm.lastAutoSyncDuration.Load(),
		LastAutoSyncError:            lastErr,
		AutoSyncCleanTicks:           dm.autoSyncCleanTicks.Load(),
		AutoSyncBackoffFactor:        dm.autoSyncBackoffFactor.Load(),
		ChaosDrops:                   dm.chaos.Drops(),
		ChaosLatencies:               dm.chaos.Latencies(),
		TombstonesActive:             dm.metrics.tombstonesActive.Load(),
		TombstonesPurged:             dm.metrics.tombstonesPurged.Load(),
		WriteQuorumFailures:          dm.metrics.writeQuorumFailures.Load(),
		WriteAcks:                    dm.metrics.writeAcks.Load(),
		WriteAttempts:                dm.metrics.writeAttempts.Load(),
		RebalancedKeys:               dm.metrics.rebalancedKeys.Load(),
		RebalanceBatches:             dm.metrics.rebalanceBatches.Load(),
		RebalanceThrottle:            dm.metrics.rebalanceThrottle.Load(),
		RebalanceLastNanos:           dm.metrics.rebalanceLastNanos.Load(),
		RebalancedReplicaDiff:        dm.metrics.rebalanceReplicaDiff.Load(),
		RebalanceReplicaDiffThrottle: dm.metrics.rebalanceReplicaDiffThrottle.Load(),
		RebalancedPrimary:            dm.metrics.rebalancedPrimary.Load(),
		MembershipVersion:            memSnap.version,
		MembersAlive:                 memSnap.alive,
		MembersSuspect:               memSnap.suspect,
		MembersDead:                  memSnap.dead,
	}
}

// DistMembershipSnapshot returns lightweight membership view (states & version).
func (dm *DistMemory) DistMembershipSnapshot() map[string]any {
	if dm.membership == nil {
		return nil
	}

	counts := map[string]int{"alive": 0, "suspect": 0, "dead": 0}
	for _, n := range dm.membership.List() {
		counts[n.State.String()]++
	}

	return map[string]any{
		"version": dm.membership.Version(),
		"counts":  counts,
	}
}

// LatencyHistograms returns a snapshot of latency bucket counts per operation (ns buckets; last bucket +Inf).
func (dm *DistMemory) LatencyHistograms() map[string][]uint64 {
	if dm.latency == nil {
		return nil
	}

	return dm.latency.snapshot()
}

// Stop terminates every background goroutine started by NewDistMemory and
// shuts down the optional HTTP server. Idempotent and safe to call concurrently
// — repeat calls are no-ops. Tests SHOULD register Stop via t.Cleanup to avoid
// goroutine leaks across `-count=N` iterations under -race.
func (dm *DistMemory) Stop(ctx context.Context) error {
	if !dm.stopped.CompareAndSwap(false, true) {
		return nil
	}

	// Cancel the lifecycle context first so in-flight HTTP handlers and
	// background loops see Done() before we start tearing down channels.
	// Background loops still listen on their stopCh below for backward
	// compatibility; new code should prefer ctx.Done() over the channel.
	if dm.lifeCancel != nil {
		dm.lifeCancel()
	}

	dm.closeBackgroundLoops()

	if dm.repairQueue != nil {
		// Drain pending entries before returning so a clean shutdown
		// dispatches in-flight repairs rather than dropping them.
		// stop() blocks until the flusher goroutine emits its final
		// flushAll and exits.
		dm.repairQueue.stop()

		dm.repairQueue = nil
	}

	// Unregister the OTel metric callback before tearing down the HTTP
	// server so the SDK does not invoke a callback against a
	// half-stopped DistMemory (Metrics() reads membership which is
	// still safe, but the snapshot would be misleading). Errors are
	// logged, not propagated — Stop is idempotent and the unregister
	// path is best-effort.
	if dm.metricRegistration != nil {
		err := dm.metricRegistration.Unregister()
		if err != nil {
			dm.logger.Error("dist meter: callback unregister failed", slog.Any("err", err))
		}

		dm.metricRegistration = nil
	}

	if dm.httpServer != nil {
		err := dm.httpServer.stop(ctx) // best-effort

		dm.httpServer = nil

		if err != nil {
			return err
		}
	}

	return nil
}

// Drain marks this node for graceful shutdown: future Set/Remove
// return sentinel.ErrDraining, /health reports HTTP 503 so external
// load balancers stop routing traffic, and the operator should
// follow up with Stop after Drain has settled. Get continues to
// serve so in-flight reads complete with consistent data.
//
// Drain is one-way and idempotent — the second call is a no-op
// (returns nil). Operators clear it by restarting the process.
//
// Returns nil today; the signature retains an error so future
// versions can wait for active replication fan-out to flush before
// returning (Phase B's hint queue makes that meaningful) without a
// breaking change.
func (dm *DistMemory) Drain(_ context.Context) error {
	if !dm.draining.CompareAndSwap(false, true) {
		return nil // already draining
	}

	dm.metrics.drains.Add(1)

	dm.logger.Info(
		"dist node draining",
		slog.String("addr", dm.nodeAddr),
	)

	return nil
}

// IsDraining reports whether Drain has been called on this node.
// Operator helper for dashboards / readiness probes that want to
// surface drain state independently of the dist HTTP endpoint.
func (dm *DistMemory) IsDraining() bool { return dm.draining.Load() }

// --- Sync helper methods (placed after exported methods to satisfy ordering linter) ---

// IsOwner reports whether this node is an owner (primary or replica) for key.
// Exported for tests / external observability (thin wrapper over internal logic).
func (dm *DistMemory) IsOwner(key string) bool {
	return dm.ownsKeyInternal(key)
}

// AddPeer adds a peer address into local membership (best-effort, no network validation).
// If the peer already exists (by address) it's ignored. Used by tests to simulate join propagation.
func (dm *DistMemory) AddPeer(address string) {
	if dm == nil || dm.membership == nil || address == "" {
		return
	}

	if dm.localNode != nil && dm.localNode.Address == address {
		return
	}

	for _, n := range dm.membership.List() {
		if n.Address == address {
			return
		}
	}

	dm.membership.Upsert(cluster.NewNode("", address))
	dm.logger.Info(
		"peer added to membership",
		slog.String("peer_addr", address),
		slog.Int("members_after", len(dm.membership.List())),
	)
}

// RemovePeer removes a peer by address (best-effort) to simulate node leave in tests.
func (dm *DistMemory) RemovePeer(address string) {
	if dm == nil || dm.membership == nil || address == "" {
		return
	}

	for _, n := range dm.membership.List() {
		if n.Address == address {
			dm.membership.Remove(n.ID)

			dm.logger.Info(
				"peer removed from membership",
				slog.String("peer_id", string(n.ID)),
				slog.String("peer_addr", address),
				slog.Int("members_after", len(dm.membership.List())),
			)

			return
		}
	}
}

// startRepairQueueIfEnabled builds + starts the read-repair queue
// when WithDistReadRepairBatch was set with a non-zero interval +
// batch size. The queue holds a closure over loadTransport so it
// always sees the currently-active transport (including chaos-wrapped
// or post-Stop nil transports).
func (dm *DistMemory) startRepairQueueIfEnabled(ctx context.Context) {
	if dm.repairBatchInterval <= 0 || dm.repairBatchSize <= 0 {
		return
	}

	dm.repairQueue = newRepairQueue(
		dm.repairBatchInterval,
		dm.repairBatchSize,
		dm.loadTransport,
		&dm.metrics,
		dm.logger,
	)
	dm.repairQueue.start(ctx)

	dm.logger.Info(
		"read-repair batching enabled",
		slog.Duration("interval", dm.repairBatchInterval),
		slog.Int("max_batch_size", dm.repairBatchSize),
	)
}

// closeBackgroundLoops closes every stopCh wired into a background
// goroutine launched by NewDistMemory. Extracted from Stop to keep
// that method under the function-length budget; ordering is not
// load-bearing (each loop's select handles its own teardown).
func (dm *DistMemory) closeBackgroundLoops() {
	stopChs := []*chan struct{}{
		&dm.stopCh,
		&dm.hintStopCh,
		&dm.gossipStopCh,
		&dm.autoSyncStopCh,
		&dm.tombStopCh,
		&dm.rebalanceStopCh,
	}

	for _, ch := range stopChs {
		if *ch != nil {
			close(*ch)

			*ch = nil
		}
	}
}

// logClusterJoin emits a single structured line summarizing the cluster
// shape this node is about to join. The most operator-visible event in
// the cluster lifecycle: one log per node startup that captures every
// knob that affects placement and replication (so log archaeology can
// reconstruct what shape the cluster believed itself to have when a
// given node booted).
func (dm *DistMemory) logClusterJoin() {
	if dm.logger == nil {
		return
	}

	peers := []string{}
	if dm.membership != nil {
		for _, n := range dm.membership.List() {
			if dm.localNode != nil && n.ID == dm.localNode.ID {
				continue
			}

			peers = append(peers, fmt.Sprintf("%s@%s", n.ID, n.Address))
		}
	}

	nodeID := ""
	nodeAddr := ""

	if dm.localNode != nil {
		nodeID = string(dm.localNode.ID)
		nodeAddr = dm.localNode.Address
	}

	dm.logger.Info(
		"cluster join: node starting",
		slog.String("node_id", nodeID),
		slog.String("node_addr", nodeAddr),
		slog.Int("replication", dm.replication),
		slog.Int("virtual_nodes", dm.virtualNodes),
		slog.Int("peers_known", len(peers)),
		slog.Any("peers", peers),
		slog.Duration("heartbeat_interval", dm.hbInterval),
		slog.Duration("rebalance_interval", dm.rebalanceInterval),
		slog.Duration("gossip_interval", dm.gossipInterval),
		slog.Duration("auto_sync_interval", dm.autoSyncInterval),
	)
}

// distMembershipSnap is the result of membershipSnapshot — bundled
// into a struct because returning four scalars hits the per-function
// result-count lint cap.
type distMembershipSnap struct {
	version uint64
	alive   int64
	suspect int64
	dead    int64
}

// membershipSnapshot returns the current membership version plus the
// count of alive/suspect/dead members. Extracted from Metrics() to
// keep that method under the function-length lint cap.
func (dm *DistMemory) membershipSnapshot() distMembershipSnap {
	if dm.membership == nil {
		return distMembershipSnap{}
	}

	out := distMembershipSnap{version: dm.membership.Version()}

	for _, n := range dm.membership.List() {
		switch n.State.String() {
		case "alive":
			out.alive++
		case "suspect":
			out.suspect++
		case "dead":
			out.dead++
		default: // ignore future states
		}
	}

	return out
}

// sortedMerkleEntries returns merkle entries sorted by key.
func (dm *DistMemory) sortedMerkleEntries() []merkleKV {
	entries := dm.merkleEntries()

	slices.SortFunc(entries, func(a, b merkleKV) int {
		return strings.Compare(a.k, b.k)
	})

	return entries
}

// resolveMissingKeys enumerates remote-only keys using in-process or HTTP listing.
func (dm *DistMemory) resolveMissingKeys(ctx context.Context, nodeID string, entries []merkleKV) map[string]struct{} {
	missing := dm.enumerateRemoteOnlyKeys(nodeID, entries)
	if len(missing) != 0 {
		return missing
	}

	httpT, ok := dm.loadTransport().(*DistHTTPTransport)
	if !ok {
		return missing
	}

	keys, kerr := httpT.ListKeys(ctx, nodeID)
	if kerr != nil || len(keys) == 0 {
		return missing
	}

	if dm.listKeysMax > 0 && len(keys) > dm.listKeysMax { // cap enforcement
		keys = keys[:dm.listKeysMax]
	}

	mset := make(map[string]struct{}, len(keys))
	for _, k := range keys { // populate
		mset[k] = struct{}{}
	}

	for _, e := range entries { // remove existing
		delete(mset, e.k)
	}

	// track number of remote-only keys discovered via fallback
	if len(mset) > 0 {
		dm.metrics.merkleKeysPulled.Add(int64(len(mset)))
	}

	return mset
}

// applyMerkleDiffs fetches and adopts keys for differing Merkle chunks.
func (dm *DistMemory) applyMerkleDiffs(
	ctx context.Context,
	nodeID string,
	entries []merkleKV,
	diffs []int,
	chunkSize int,
) {
	for _, ci := range diffs {
		start := ci * chunkSize
		if start >= len(entries) {
			continue
		}

		end := min(start+chunkSize, len(entries))

		for _, e := range entries[start:end] {
			dm.fetchAndAdopt(ctx, nodeID, e.k)
		}
	}
}

// loadTransport returns the currently configured transport, or nil. Safe to
// call concurrently with storeTransport.
func (dm *DistMemory) loadTransport() DistTransport {
	if slot := dm.transport.Load(); slot != nil {
		return slot.t
	}

	return nil
}

// storeTransport replaces the active transport. Safe to call concurrently
// with loadTransport.
//
// When a Chaos is configured via WithDistChaos, the supplied transport is
// transparently wrapped before being stored — every chaos roll happens
// inside the wrapper so callers (Forward*, Health, Gossip, FetchMerkle)
// don't have to know chaos exists. Double-wrapping is detected and
// short-circuited, so this is safe to call from option appliers in any
// order.
func (dm *DistMemory) storeTransport(t DistTransport) {
	if dm.chaos != nil && t != nil {
		if _, already := t.(*chaosTransport); !already {
			t = newChaosTransport(t, dm.chaos)
		}
	}

	dm.transport.Store(&distTransportSlot{t: t})
}

// enumerateRemoteOnlyKeys returns keys present only on the remote side (best-effort, in-process only).
func (dm *DistMemory) enumerateRemoteOnlyKeys(nodeID string, local []merkleKV) map[string]struct{} {
	missing := make(map[string]struct{})

	ip, ok := dm.loadTransport().(*InProcessTransport)
	if !ok {
		return missing
	}

	remote, ok := ip.backends[nodeID]
	if !ok {
		return missing
	}

	for _, shard := range remote.shards {
		if shard == nil {
			continue
		}

		for k := range shard.items.All() {
			missing[k] = struct{}{}
		}
	}

	for _, e := range local { // remove any that we already have
		delete(missing, e.k)
	}

	return missing
}

// fetchAndAdopt pulls a key from a remote node and adopts it if it's newer or absent locally.
func (dm *DistMemory) fetchAndAdopt(ctx context.Context, nodeID, key string) {
	transport := dm.loadTransport()
	if transport == nil {
		return
	}

	it, ok, gerr := transport.ForwardGet(ctx, nodeID, key)
	if gerr != nil { // remote failure: ignore
		return
	}

	sh := dm.shardFor(key)

	if !ok { // remote missing key (could be delete) -> adopt tombstone locally if we still have it
		if _, hasTomb := sh.tombs[key]; hasTomb { // already have tombstone
			return
		}

		if cur, okLocal := sh.items.Get(key); okLocal { // create tombstone advancing version
			sh.items.Remove(key)

			nextVer := cur.Version + 1

			sh.tombs[key] = tombstone{version: nextVer, origin: string(dm.localNode.ID), at: time.Now()}
			dm.metrics.tombstonesActive.Store(dm.countTombstones())
		}

		return
	}

	if tomb, hasTomb := sh.tombs[key]; hasTomb {
		// If tombstone version newer or equal, do not resurrect
		if tomb.version >= it.Version {
			return
		}

		// remote has newer version; clear tombstone (key resurrected intentionally)
		delete(sh.tombs, key)
		dm.metrics.tombstonesActive.Store(dm.countTombstones())
	}

	if cur, okLocal := sh.items.Get(key); !okLocal || it.Version > cur.Version {
		dm.applySet(ctx, it, false)
		dm.metrics.merkleKeysPulled.Add(1)
	}
}

// merkleEntries gathers key/version pairs from all shards.
func (dm *DistMemory) merkleEntries() []merkleKV {
	entries := make([]merkleKV, 0, merklePreallocEntries)

	for _, shard := range dm.shards {
		if shard == nil {
			continue
		}

		for k, it := range shard.items.All() {
			entries = append(entries, merkleKV{k: k, v: it.Version})
		}

		for k, ts := range shard.tombs { // include tombstones
			entries = append(entries, merkleKV{k: k, v: ts.version})
		}
	}

	return entries
}

// startTombstoneSweeper launches periodic compaction if configured.
func (dm *DistMemory) startTombstoneSweeper() {
	if dm.tombstoneTTL <= 0 || dm.tombstoneSweepInt <= 0 {
		return
	}

	stopCh := make(chan struct{})

	dm.tombStopCh = stopCh
	go func(stopCh <-chan struct{}) {
		ticker := time.NewTicker(dm.tombstoneSweepInt)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				purged := dm.compactTombstones()
				if purged > 0 {
					dm.metrics.tombstonesPurged.Add(purged)
				}

				dm.metrics.tombstonesActive.Store(dm.countTombstones())

			case <-stopCh:
				return
			}
		}
	}(stopCh)
}

// compactTombstones removes expired tombstones based on TTL, returns count purged.
func (dm *DistMemory) compactTombstones() int64 {
	if dm.tombstoneTTL <= 0 {
		return 0
	}

	now := time.Now()

	var purged int64

	for _, sh := range dm.shards {
		if sh == nil {
			continue
		}

		for k, ts := range sh.tombs {
			if now.Sub(ts.at) >= dm.tombstoneTTL {
				delete(sh.tombs, k)

				purged++
			}
		}
	}

	return purged
}

// countTombstones returns approximate current count.
func (dm *DistMemory) countTombstones() int64 {
	var total int64

	for _, sh := range dm.shards {
		if sh == nil {
			continue
		}

		total += int64(len(sh.tombs))
	}

	return total
}

// Rebalancing (Phase 3 initial implementation).
func (dm *DistMemory) startRebalancerIfEnabled(ctx context.Context) {
	if dm.rebalanceInterval <= 0 || dm.membership == nil || dm.ring == nil {
		return
	}

	if dm.rebalanceBatchSize <= 0 {
		dm.rebalanceBatchSize = defaultRebalanceBatchSize
	}

	if dm.rebalanceMaxConcurrent <= 0 {
		dm.rebalanceMaxConcurrent = defaultRebalanceMaxConcurrent
	}

	stopCh := make(chan struct{})

	dm.rebalanceStopCh = stopCh

	go dm.rebalanceLoop(ctx, stopCh)

	dm.logger.Info(
		"rebalance loop started",
		slog.Duration("interval", dm.rebalanceInterval),
		slog.Int("batch_size", dm.rebalanceBatchSize),
		slog.Int("max_concurrent", dm.rebalanceMaxConcurrent),
		slog.Int("replica_diff_max_per_tick", dm.replicaDiffMaxPerTick),
	)
}

func (dm *DistMemory) rebalanceLoop(ctx context.Context, stopCh <-chan struct{}) {
	ticker := time.NewTicker(dm.rebalanceInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			dm.runRebalanceTick(ctx)
		case <-stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// runRebalanceTick performs a lightweight ownership diff and migrates keys best-effort.
func (dm *DistMemory) runRebalanceTick(ctx context.Context) {
	mv := uint64(0)
	if dm.membership != nil {
		mv = dm.membership.Version()
	}

	// Always perform a scan so that throttled prior ticks or new key inserts
	// can be migrated even if membership version hasn't advanced.
	start := time.Now()

	candidates := dm.collectRebalanceCandidates()
	if len(candidates) > 0 {
		dm.migrateItems(ctx, candidates)
	}

	// After migration attempts, replicate to any new replicas introduced for keys where we remain primary.
	dm.replicateNewReplicas(ctx)

	// Perform shedding cleanup after replica diff (delete local copies after grace once we are no longer owner).
	dm.shedRemovedKeys()

	dm.metrics.rebalanceLastNanos.Store(time.Since(start).Nanoseconds())
	dm.lastRebalanceVersion.Store(mv)
}

// collectRebalanceCandidates scans shards for items whose primary ownership changed.
// Note: we copy items (not pointers) to avoid pointer-to-loop-variable issues.
func (dm *DistMemory) collectRebalanceCandidates() []cache.Item {
	if len(dm.shards) == 0 {
		return nil
	}

	const capHint = 1024

	out := make([]cache.Item, 0, capHint)
	for _, sh := range dm.shards {
		if sh == nil {
			continue
		}

		for _, it := range sh.items.All() { // snapshot iteration
			cloned := *it
			if dm.shouldRebalance(sh, &cloned) {
				out = append(out, cloned)
			}
		}
	}

	return out
}

// shouldRebalance determines if the item should be migrated away.
// Triggers when this node lost all ownership or was previously primary and is no longer.
func (dm *DistMemory) shouldRebalance(sh *distShard, it *cache.Item) bool {
	if !dm.ownsKeyInternal(it.Key) { // lost all ownership
		if dm.removalGracePeriod > 0 && sh.removedAt != nil { // record timestamp if not already
			sh.removedAtMu.Lock()

			if _, exists := sh.removedAt[it.Key]; !exists {
				sh.removedAt[it.Key] = time.Now()
			}

			sh.removedAtMu.Unlock()
		}

		return true
	}

	if dm.ring == nil || sh.originalPrimary == nil { // nothing else to compare
		return false
	}

	owners := dm.ring.Lookup(it.Key)
	if len(owners) == 0 { // ring empty => treat as owned
		return false
	}

	curPrimary := owners[0]

	sh.originalPrimaryMu.RLock()

	prevPrimary, hadPrev := sh.originalPrimary[it.Key]
	sh.originalPrimaryMu.RUnlock()

	if !hadPrev { // no historical record
		return false
	}

	return prevPrimary == dm.localNode.ID && curPrimary != dm.localNode.ID
}

// replicateNewReplicas scans for keys where this node is still primary but new replica owners were added since first observation.
// It forwards the current item to newly added replicas (best-effort) and updates originalOwners snapshot.
func (dm *DistMemory) replicateNewReplicas(ctx context.Context) {
	if dm.ring == nil || dm.loadTransport() == nil {
		return
	}

	limit := dm.replicaDiffMaxPerTick

	processed := 0
	for _, sh := range dm.shards {
		if sh == nil {
			continue
		}

		processed = dm.replDiffShard(ctx, sh, processed, limit)
		if limit > 0 && processed >= limit {
			return
		}
	}
}

func (dm *DistMemory) replDiffShard(ctx context.Context, sh *distShard, processed, limit int) int {
	// Snapshot under the iter.Seq2 RLock so we don't hold the shard read
	// lock across sendReplicaDiff's network I/O. Capacity is conservative
	// (Count may drift between snapshot start and end, but the slice
	// grows as needed).
	snapshot := make([]cache.Item, 0, sh.items.Count())
	for _, it := range sh.items.All() {
		snapshot = append(snapshot, *it)
	}

	for i := range snapshot {
		if limit > 0 && processed >= limit {
			return processed
		}

		it := &snapshot[i]

		owners := dm.ring.Lookup(it.Key)
		if len(owners) == 0 || owners[0] != dm.localNode.ID {
			dm.maybeRecordRemoval(sh, it.Key)

			continue
		}

		if !dm.ensureOwnerBaseline(sh, it.Key, owners) {
			continue
		} // baseline created

		newRepls := dm.computeNewReplicas(sh, it.Key, owners)
		if len(newRepls) == 0 {
			continue
		}

		processed = dm.sendReplicaDiff(ctx, it, newRepls, processed, limit)
		dm.setOwnerBaseline(sh, it.Key, owners)
	}

	return processed
}

func (*DistMemory) ensureOwnerBaseline(sh *distShard, key string, owners []cluster.NodeID) bool { // returns existed
	sh.originalOwnersMu.RLock()

	_, had := sh.originalOwners[key]
	sh.originalOwnersMu.RUnlock()

	if had {
		return true
	}

	sh.originalOwnersMu.Lock()

	if sh.originalOwners[key] == nil {
		cp := make([]cluster.NodeID, len(owners))
		copy(cp, owners)

		sh.originalOwners[key] = cp
	}

	sh.originalOwnersMu.Unlock()

	return false
}

func (*DistMemory) computeNewReplicas(sh *distShard, key string, owners []cluster.NodeID) []cluster.NodeID {
	sh.originalOwnersMu.RLock()

	prev := sh.originalOwners[key]
	sh.originalOwnersMu.RUnlock()

	prevSet := make(map[cluster.NodeID]struct{}, len(prev))
	for _, p := range prev {
		prevSet[p] = struct{}{}
	}

	var out []cluster.NodeID

	for _, o := range owners[1:] {
		if _, ok := prevSet[o]; !ok {
			out = append(out, o)
		}
	}

	return out
}

func (dm *DistMemory) sendReplicaDiff(
	ctx context.Context,
	it *cache.Item,
	repls []cluster.NodeID,
	processed, limit int,
) int {
	transport := dm.loadTransport()
	if transport == nil {
		return processed
	}

	for _, rid := range repls {
		if rid == dm.localNode.ID {
			continue
		}

		_ = transport.ForwardSet(ctx, string(rid), it, false)

		dm.metrics.replicaFanoutSet.Add(1)
		dm.metrics.rebalancedKeys.Add(1)
		dm.metrics.rebalanceReplicaDiff.Add(1)

		processed++
		if limit > 0 && processed >= limit {
			dm.metrics.rebalanceReplicaDiffThrottle.Add(1)

			return processed
		}
	}

	return processed
}

func (*DistMemory) setOwnerBaseline(sh *distShard, key string, owners []cluster.NodeID) {
	sh.originalOwnersMu.Lock()

	cp := make([]cluster.NodeID, len(owners))
	copy(cp, owners)

	sh.originalOwners[key] = cp
	sh.originalOwnersMu.Unlock()
}

func (dm *DistMemory) maybeRecordRemoval(sh *distShard, key string) {
	if dm.removalGracePeriod <= 0 || sh.removedAt == nil {
		return
	}

	if dm.ownsKeyInternal(key) {
		return
	}

	sh.removedAtMu.Lock()

	if _, ok := sh.removedAt[key]; !ok {
		sh.removedAt[key] = time.Now()
	}

	sh.removedAtMu.Unlock()
}

// migrateItems concurrently migrates items in batches respecting configured limits.
func (dm *DistMemory) migrateItems(ctx context.Context, items []cache.Item) {
	if len(items) == 0 {
		return
	}

	sem := make(chan struct{}, dm.rebalanceMaxConcurrent)

	var wg sync.WaitGroup

	for start := 0; start < len(items); {
		end := min(start+dm.rebalanceBatchSize, len(items))

		batch := items[start:end]

		start = end

		select {
		case sem <- struct{}{}:
		default:
			// saturated; record throttle and then block
			dm.metrics.rebalanceThrottle.Add(1)

			sem <- struct{}{}
		}

		batchItems := batch
		wg.Go(func() {
			defer func() { <-sem }()

			dm.metrics.rebalanceBatches.Add(1)

			for i := range batchItems {
				itm := batchItems[i] // value copy
				dm.migrateIfNeeded(ctx, &itm)
			}
		})
	}

	wg.Wait()
}

// migrateIfNeeded forwards item to new primary if this node no longer owns it.
func (dm *DistMemory) migrateIfNeeded(ctx context.Context, item *cache.Item) {
	owners := dm.lookupOwners(item.Key)
	if len(owners) == 0 || owners[0] == dm.localNode.ID {
		return
	}

	transport := dm.loadTransport()
	if transport == nil {
		return
	}

	// increment metrics once per attempt (ownership changed). Success is best-effort.
	dm.metrics.rebalancedKeys.Add(1)
	dm.metrics.rebalancedPrimary.Add(1)

	// Forward the item to the new primary. On failure, hand the item
	// to the hint-replay queue keyed by the new primary's node ID:
	// the replay loop will retry on its configured schedule until the
	// hint TTL expires. Pre-Phase-B this dropped silently — operators
	// saw vanished keys after a rebalance tick when the new primary
	// was briefly unreachable. Note: replay calls ForwardSet with
	// replicate=false; the new primary's own rebalance/replica-diff
	// scan re-fans-out to its replicas eventually.
	migrationErr := transport.ForwardSet(ctx, string(owners[0]), item, true)
	if migrationErr != nil {
		dm.logger.Info(
			"rebalance migration forward failed; queued for hint replay",
			slog.String("key", item.Key),
			slog.String("new_primary", string(owners[0])),
			slog.Any("err", migrationErr),
		)

		dm.queueHint(string(owners[0]), item, hintSourceMigration)
	}

	// Update originalPrimary so we don't recount repeatedly.
	sh := dm.shardFor(item.Key)
	if sh.originalPrimary != nil {
		sh.originalPrimaryMu.Lock()

		sh.originalPrimary[item.Key] = owners[0]
		sh.originalPrimaryMu.Unlock()
	}

	// Record removal timestamp for potential shedding if we are no longer owner at all.
	if dm.removalGracePeriod > 0 && !dm.ownsKeyInternal(item.Key) && sh.removedAt != nil {
		sh.removedAtMu.Lock()

		if _, exists := sh.removedAt[item.Key]; !exists {
			sh.removedAt[item.Key] = time.Now()
		}

		sh.removedAtMu.Unlock()
	}
}

// shedRemovedKeys deletes keys for which this node is no longer an owner after grace period.
// Best-effort: we iterate shards, check removal timestamps, and remove local copy if grace elapsed.
func (dm *DistMemory) shedRemovedKeys() {
	if dm.removalGracePeriod <= 0 {
		return
	}

	now := time.Now()
	for _, sh := range dm.shards {
		if sh != nil {
			dm.shedShard(sh, now)
		}
	}
}

func (dm *DistMemory) shedShard(sh *distShard, now time.Time) {
	if sh.removedAt == nil {
		return
	}

	grace := dm.removalGracePeriod

	sh.removedAtMu.Lock()

	var dels []string

	for k, at := range sh.removedAt {
		if now.Sub(at) >= grace {
			dels = append(dels, k)
			delete(sh.removedAt, k)
		}
	}

	sh.removedAtMu.Unlock()

	for _, k := range dels {
		if !dm.ownsKeyInternal(k) {
			sh.items.Remove(k)
		}
	}
}

func encodeUint64BigEndian(buf []byte, v uint64) {
	for i := merkleVersionBytes - 1; i >= 0; i-- { // big endian for deterministic hashing
		buf[i] = byte(v)

		v >>= shiftPerByte
	}
}

// foldMerkle reduces leaf hashes into a single root using a binary tree.
func foldMerkle(leaves [][]byte, hasher hash.Hash) []byte {
	if len(leaves) == 0 {
		return nil
	}

	level := leaves
	for len(level) > 1 {
		next := make([][]byte, 0, (len(level)+1)/2)
		for i := 0; i < len(level); i += 2 {
			if i+1 == len(level) { // odd node promoted
				next = append(next, append([]byte(nil), level[i]...))

				break
			}

			hasher.Reset()

			_, _ = hasher.Write(level[i])
			_, _ = hasher.Write(level[i+1])
			next = append(next, append([]byte(nil), hasher.Sum(nil)...))
		}

		level = next
	}

	return level[0]
}

// ensureShardConfig initializes shards respecting configured shardCount.
func (dm *DistMemory) ensureShardConfig() {
	if dm.shardCount <= 0 {
		dm.shardCount = defaultDistShardCount
	}

	for len(dm.shards) < dm.shardCount { // grow
		// originalPrimary & originalOwners protected by their mutexes for concurrent rebalance scans/migrations.
		dm.shards = append(dm.shards,
			&distShard{
				items:           cache.New(),
				tombs:           make(map[string]tombstone),
				originalPrimary: make(map[string]cluster.NodeID),
				originalOwners:  make(map[string][]cluster.NodeID),
				removedAt:       make(map[string]time.Time),
			})
	}
}

// initMembershipIfNeeded sets up membership/ring and local node defaults.
func (dm *DistMemory) initMembershipIfNeeded() {
	if dm.membership == nil {
		dm.initStandaloneMembership()
		dm.installEventBusObserver()

		return
	}

	if dm.localNode == nil {
		dm.localNode = cluster.NewNode("", "local")
	}

	dm.membership.Upsert(dm.localNode)

	dm.ring = dm.membership.Ring()
	if dm.nodeAddr == "" && dm.localNode != nil {
		dm.nodeAddr = dm.localNode.Address
	}

	dm.installEventBusObserver()
}

// tryStartHTTP starts internal HTTP transport if not provided.
//
// The bind ctx (parameter) controls the listener.Listen call only; the
// server stores dm.lifeCtx as its handler-side operation context, so
// in-flight backend work observes Stop's cancellation independent of
// the constructor ctx the caller supplied.
func (dm *DistMemory) tryStartHTTP(ctx context.Context) {
	if dm.loadTransport() != nil || dm.nodeAddr == "" {
		return
	}

	// Resolve once so server and auto-created client share the same
	// timeouts / body caps — a request that the server would reject as
	// too large is also one the client should not attempt to send.
	limits := dm.httpLimits.withDefaults()

	server := newDistHTTPServer(dm.nodeAddr, limits, dm.httpAuth)

	server.ctx = dm.lifeCtx // handler-side cancellation tied to Stop
	server.logger = dm.logger

	err := server.start(ctx, dm)
	if err != nil { // best-effort, but the operator must see this
		dm.logger.Error(
			"dist HTTP listener bind failed",
			slog.String("addr", dm.nodeAddr),
			slog.Any("err", err),
		)

		return
	}

	dm.httpServer = server

	dm.logger.Info(
		"dist HTTP listener started",
		slog.String("addr", dm.nodeAddr),
		slog.Bool("tls", limits.TLSConfig != nil),
		slog.Bool("auth", dm.httpAuth.inboundConfigured()),
	)

	resolver := dm.makePeerURLResolver(limits)

	dm.storeTransport(NewDistHTTPTransportWithAuth(limits, dm.httpAuth, resolver))
}

// makePeerURLResolver returns a resolver that maps node IDs to base URLs.
// The scheme follows whether TLS is configured: https:// when
// limits.TLSConfig is non-nil, http:// otherwise. Extracted from
// tryStartHTTP / EnableHTTPForTest so both share one source of truth for
// scheme selection — getting it wrong causes the client to dial plaintext
// against a TLS listener (or vice versa).
func (dm *DistMemory) makePeerURLResolver(limits DistHTTPLimits) func(string) (string, bool) {
	scheme := "http://"
	if limits.TLSConfig != nil {
		scheme = "https://"
	}

	return func(nodeID string) (string, bool) {
		if dm.membership != nil {
			for _, n := range dm.membership.List() {
				if string(n.ID) == nodeID {
					return scheme + n.Address, true
				}
			}
		}

		if dm.localNode != nil && string(dm.localNode.ID) == nodeID {
			return scheme + dm.localNode.Address, true
		}

		return "", false
	}
}

// startHeartbeatIfEnabled launches heartbeat loop if configured.
func (dm *DistMemory) startHeartbeatIfEnabled(ctx context.Context) {
	if dm.hbInterval > 0 && dm.loadTransport() != nil {
		stopCh := make(chan struct{})

		dm.stopCh = stopCh
		go dm.heartbeatLoop(ctx, stopCh)

		dm.logger.Info(
			"heartbeat loop started",
			slog.Duration("interval", dm.hbInterval),
			slog.Duration("suspect_after", dm.hbSuspectAfter),
			slog.Duration("dead_after", dm.hbDeadAfter),
			slog.Int("indirect_probe_k", dm.indirectProbeK),
		)
	}

	dm.startHeartbeatEventPublisher()
}

// lookupOwners returns ring owners slice for a key (nil if no ring).
func (dm *DistMemory) lookupOwners(key string) []cluster.NodeID {
	if dm.ring == nil {
		return nil
	}

	return dm.ring.Lookup(key)
}

// requiredAcks computes required acknowledgements for given consistency level.
func (*DistMemory) requiredAcks(total int, lvl ConsistencyLevel) int {
	//nolint:revive
	switch lvl {
	case ConsistencyAll:
		return total
	case ConsistencyQuorum:
		return (total / 2) + 1
	case ConsistencyOne:
		return 1 // identical-switch-branches kept for clarity.
	default:
		return 1
	}
}

// getOne fetches from a single owner path.
func (dm *DistMemory) getOne(ctx context.Context, key string, owners []cluster.NodeID) (*cache.Item, bool) {
	for idx, oid := range owners { // iterate owners until hit
		if it, ok := dm.tryLocalGet(key, idx, oid); ok {
			return it, true
		}

		if it, ok := dm.tryRemoteGet(ctx, key, idx, oid); ok {
			return it, true
		}
	}

	return nil, false
}

// tryLocalGet attempts local shard lookup when oid is local; returns item if found.
func (dm *DistMemory) tryLocalGet(key string, idx int, oid cluster.NodeID) (*cache.Item, bool) {
	if oid != dm.localNode.ID { // not local owner
		return nil, false
	}

	if it, ok := dm.shardFor(key).items.GetCopy(key); ok {
		if idx > 0 { // promotion
			dm.metrics.readPrimaryPromote.Add(1)
		}

		return it, true
	}

	return nil, false
}

// tryRemoteGet attempts remote fetch for given owner; includes promotion + repair.
func (dm *DistMemory) tryRemoteGet(ctx context.Context, key string, idx int, oid cluster.NodeID) (*cache.Item, bool) {
	transport := dm.loadTransport()
	if oid == dm.localNode.ID || transport == nil { // skip local path or missing transport
		return nil, false
	}

	dm.metrics.forwardGet.Add(1)

	it, ok, err := transport.ForwardGet(ctx, string(oid), key)
	if errors.Is(err, sentinel.ErrBackendNotFound) { // owner unreachable -> promotion scenario
		if idx == 0 { // primary missing
			dm.metrics.readPrimaryPromote.Add(1)
		}

		return nil, false
	}

	if !ok { // miss
		return nil, false
	}

	if idx > 0 { // promotion occurred
		dm.metrics.readPrimaryPromote.Add(1)
	}

	// read repair: if we're an owner but local missing, replicate
	if dm.ownsKeyInternal(key) {
		if _, ok2 := dm.shardFor(key).items.Get(key); !ok2 {
			cloned := *it
			dm.applySet(ctx, &cloned, false)
			dm.metrics.readRepair.Add(1)
		}
	}

	return it, true
}

// getWithConsistency performs quorum/all reads.
func (dm *DistMemory) getWithConsistency(ctx context.Context, key string, owners []cluster.NodeID) (*cache.Item, bool) {
	needed := dm.requiredAcks(len(owners), dm.readConsistency)
	acks := 0

	var chosen *cache.Item

	// gather results sequentially until quorum reached, tracking stale owners
	staleOwners := dm.collectQuorum(ctx, key, owners, needed, &chosen, &acks)
	if acks < needed || chosen == nil {
		return nil, false
	}

	dm.repairStaleOwners(ctx, key, chosen, staleOwners)
	dm.repairReplicas(ctx, key, chosen, owners)

	return chosen, true
}

// collectQuorum iterates owners, updates chosen item and acks, returns owners needing repair.
func (dm *DistMemory) collectQuorum(
	ctx context.Context,
	key string,
	owners []cluster.NodeID,
	needed int,
	chosen **cache.Item,
	acks *int,
) []cluster.NodeID {
	stale := make([]cluster.NodeID, 0, len(owners))
	for idx, oid := range owners {
		it, ok := dm.fetchOwner(ctx, key, idx, oid)
		if !ok {
			continue
		}

		prev := *chosen

		*chosen = dm.chooseNewer(*chosen, it)
		*acks++

		if prev != nil && *chosen != prev {
			stale = append(stale, oid)
		}

		if *acks >= needed && *chosen != nil { // early break
			break
		}
	}

	return stale
}

// repairStaleOwners issues best-effort targeted repairs for owners identified as stale.
func (dm *DistMemory) repairStaleOwners(
	ctx context.Context,
	key string,
	chosen *cache.Item,
	staleOwners []cluster.NodeID,
) {
	transport := dm.loadTransport()
	if transport == nil || chosen == nil {
		return
	}

	// Route through repairRemoteReplica so this path benefits from
	// the same probe-drop + opt-in batching as the primary
	// read-repair path. The send-and-let-receiver-noop pattern
	// is safe because applySet on the receiver version-compares.
	for _, oid := range staleOwners {
		if oid == dm.localNode.ID { // local handled in repairReplicas
			continue
		}

		dm.repairRemoteReplica(ctx, key, chosen, oid)
	}
}

// fetchOwner attempts to fetch item from given owner (local or remote) updating metrics.
func (dm *DistMemory) fetchOwner(ctx context.Context, key string, idx int, oid cluster.NodeID) (*cache.Item, bool) {
	if oid == dm.localNode.ID { // local
		if it, ok := dm.shardFor(key).items.GetCopy(key); ok {
			return it, true
		}

		return nil, false
	}

	transport := dm.loadTransport()
	if transport == nil {
		return nil, false
	}

	it, ok, err := transport.ForwardGet(ctx, string(oid), key)
	if errors.Is(err, sentinel.ErrBackendNotFound) { // promotion
		if idx == 0 {
			dm.metrics.readPrimaryPromote.Add(1)
		}

		return nil, false
	}

	if !ok {
		return nil, false
	}

	if idx > 0 { // earlier owner skipped
		dm.metrics.readPrimaryPromote.Add(1)
	}

	return it, true
}

// replicateTo sends writes to replicas (best-effort) returning ack count.
func (dm *DistMemory) replicateTo(ctx context.Context, item *cache.Item, replicas []cluster.NodeID) int {
	transport := dm.loadTransport()
	if transport == nil {
		return 0
	}

	acks := 0
	for _, oid := range replicas {
		if oid == dm.localNode.ID {
			continue
		}

		// Reuse the per-peer child-span helper so the primary-path
		// fan-out emits the same `dist.replicate.set` spans that
		// applySet's incoming-replica fan-out emits — operators see a
		// uniform trace tree regardless of which path drove the
		// replication.
		err := dm.replicateSetWithSpan(ctx, transport, oid, item)
		if err == nil {
			acks++

			continue
		}

		// Queue a hint for ANY transport error — pre-Phase-B this was
		// gated on ErrBackendNotFound only, so transient HTTP failures
		// (timeout, 5xx, connection reset) silently dropped replicas.
		// The hint TTL bounds total retry time, so a target that's
		// permanently gone still drains rather than ballooning.
		dm.queueHint(string(oid), item, hintSourceReplication)
	}

	return acks
}

// getWithConsistencyParallel performs parallel owner fan-out until quorum/all reached.
func (dm *DistMemory) getWithConsistencyParallel(
	ctx context.Context,
	key string,
	owners []cluster.NodeID,
) (*cache.Item, bool) {
	needed := dm.requiredAcks(len(owners), dm.readConsistency)

	type res struct {
		owner cluster.NodeID
		it    *cache.Item
		ok    bool
	}

	ch := make(chan res, len(owners))

	ctxFetch, cancel := context.WithCancel(ctx)
	defer cancel()

	for idx, oid := range owners { // launch all with proper capture (Go <1.22 style)
		idxLocal, oidLocal := idx, oid
		go func(i int, o cluster.NodeID) {
			it, ok := dm.fetchOwner(ctxFetch, key, i, o)
			ch <- res{owner: o, it: it, ok: ok}
		}(idxLocal, oidLocal)
	}

	acks := 0

	var chosen *cache.Item

	staleOwners := make([]cluster.NodeID, 0, len(owners))

	for range owners {
		resp := <-ch
		if !resp.ok {
			continue
		}

		prev := chosen

		chosen = dm.chooseNewer(chosen, resp.it)
		acks++

		if prev != nil && chosen != prev { // newer version found, mark new owner for targeted check (mirrors sequential path semantics)
			staleOwners = append(staleOwners, resp.owner)
		}

		if acks >= needed && chosen != nil { // early satisfied
			cancel()

			break
		}
	}

	if acks < needed || chosen == nil {
		return nil, false
	}

	// targeted repairs for owners involved in version advancement (best-effort)
	dm.repairStaleOwners(ctx, key, chosen, staleOwners)

	// full repair across all owners to ensure convergence
	dm.repairReplicas(ctx, key, chosen, owners)

	return chosen, true
}

// --- Hinted handoff implementation ---.
func (dm *DistMemory) queueHint(nodeID string, item *cache.Item, source hintSource) { // reduced complexity
	if dm.hintTTL <= 0 {
		return
	}

	size := dm.approxHintSize(item)

	dm.hintsMu.Lock()

	if dm.hints == nil {
		dm.hints = make(map[string][]hintedEntry)
	}

	queue := dm.hints[nodeID]

	if dm.hintMaxPerNode > 0 && len(queue) >= dm.hintMaxPerNode { // drop oldest
		dropped := queue[0]

		queue = queue[1:]

		dm.adjustHintAccounting(-1, -dropped.size)
	}

	if (dm.hintMaxTotal > 0 && dm.hintTotal >= dm.hintMaxTotal) || (dm.hintMaxBytes > 0 && dm.hintBytes+size > dm.hintMaxBytes) {
		dm.hintsMu.Unlock()
		dm.metrics.hintedGlobalDropped.Add(1)

		if source == hintSourceMigration {
			dm.metrics.migrationHintDropped.Add(1)
		}

		return
	}

	cloned := *item

	now := time.Now()

	queue = append(queue, hintedEntry{
		item:     &cloned,
		queuedAt: now,
		expire:   now.Add(dm.hintTTL),
		size:     size,
		source:   source,
	})
	dm.hints[nodeID] = queue
	dm.adjustHintAccounting(1, size)

	// Snapshot under the lock — pre-Phase-B this read happened after
	// Unlock and raced with adjustHintAccounting in the replay loop.
	bytesNow := dm.hintBytes

	dm.hintsMu.Unlock()

	dm.metrics.hintedQueued.Add(1)
	dm.metrics.hintedBytes.Store(bytesNow)

	if source == hintSourceMigration {
		dm.metrics.migrationHintQueued.Add(1)
	}
}

// approxHintSize estimates the size of a hinted item for global caps.
func (dm *DistMemory) approxHintSize(item *cache.Item) int64 { // receiver retained for symmetry; may use config later
	_ = dm // acknowledge receiver intentionally (satisfy lint under current rule set)

	if item == nil {
		return 0
	}

	var total int64

	total += int64(len(item.Key))

	switch v := item.Value.(type) {
	case string:
		total += int64(len(v))
	case []byte:
		total += int64(len(v))
	default:
		b, err := json.Marshal(v)
		if err == nil {
			total += int64(len(b))
		}
	}

	return total
}

// adjustHintAccounting mutates counters; call with lock held.
func (dm *DistMemory) adjustHintAccounting(countDelta int, bytesDelta int64) {
	dm.hintTotal += countDelta
	dm.hintBytes += bytesDelta

	if dm.hintTotal < 0 {
		dm.hintTotal = 0
	}

	if dm.hintBytes < 0 {
		dm.hintBytes = 0
	}
}

func (dm *DistMemory) startHintReplayIfEnabled(ctx context.Context) {
	if dm.hintReplayInt <= 0 || dm.hintTTL <= 0 {
		return
	}

	stopCh := make(chan struct{})

	dm.hintStopCh = stopCh
	go dm.hintReplayLoop(ctx, stopCh)

	dm.logger.Info(
		"hint replay loop started",
		slog.Duration("interval", dm.hintReplayInt),
		slog.Duration("hint_ttl", dm.hintTTL),
	)
}

func (dm *DistMemory) hintReplayLoop(ctx context.Context, stopCh <-chan struct{}) {
	ticker := time.NewTicker(dm.hintReplayInt)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			dm.replayHints(ctx)
		case <-stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (dm *DistMemory) replayHints(ctx context.Context) { // reduced cognitive complexity
	if dm.loadTransport() == nil {
		return
	}

	now := time.Now()

	dm.hintsMu.Lock()

	for nodeID, queue := range dm.hints {
		out := queue[:0]

		for _, hintEntry := range queue { // renamed for clarity
			action := dm.processHint(ctx, nodeID, hintEntry, now)
			//nolint:revive // identical-switch-branches rule disabled for clarity
			switch action { // 0 keep, 1 remove
			case 0:
				out = append(out, hintEntry)
			case 1:
				dm.adjustHintAccounting(-1, -hintEntry.size)

			default: // defensive future-proofing
				out = append(out, hintEntry)
			}
		}

		if len(out) == 0 {
			delete(dm.hints, nodeID)
		} else {
			dm.hints[nodeID] = out
		}
	}

	dm.metrics.hintedBytes.Store(dm.hintBytes)
	dm.hintsMu.Unlock()
}

// processHint returns 0=keep,1=remove.
func (dm *DistMemory) processHint(ctx context.Context, nodeID string, entry hintedEntry, now time.Time) int {
	if now.After(entry.expire) {
		dm.metrics.hintedExpired.Add(1)

		if entry.source == hintSourceMigration {
			dm.metrics.migrationHintExpired.Add(1)
		}

		return 1
	}

	transport := dm.loadTransport()
	if transport == nil {
		return 0
	}

	err := transport.ForwardSet(ctx, nodeID, entry.item, false)
	if err == nil {
		dm.metrics.hintedReplayed.Add(1)

		if entry.source == hintSourceMigration {
			dm.metrics.migrationHintReplayed.Add(1)

			// last_age_ns reflects queue residency of the most-recently
			// replayed migration hint. Operators read this as "how long did
			// the last migration retry wait for the new primary to come
			// back?" — a direct signal of cross-node reachability during
			// rolling deploys.
			if !entry.queuedAt.IsZero() {
				dm.metrics.migrationHintLastAgeNanos.Store(now.Sub(entry.queuedAt).Nanoseconds())
			}
		}

		return 1
	}

	if errors.Is(err, sentinel.ErrBackendNotFound) { // keep – backend still absent
		return 0
	}

	dm.metrics.hintedDropped.Add(1)

	if entry.source == hintSourceMigration {
		dm.metrics.migrationHintDropped.Add(1)
	}

	dm.logger.Warn(
		"hint dropped after replay error",
		slog.String("peer_id", nodeID),
		slog.String("key", entry.item.Key),
		slog.Any("err", err),
	)

	return 1
}

// --- Simple gossip (in-process only) ---.
func (dm *DistMemory) startGossipIfEnabled() {
	if dm.gossipInterval <= 0 {
		return
	}

	stopCh := make(chan struct{})

	dm.gossipStopCh = stopCh
	go dm.gossipLoop(stopCh)

	dm.logger.Info(
		"gossip loop started",
		slog.Duration("interval", dm.gossipInterval),
	)
}

// startAutoSyncIfEnabled launches periodic merkle syncs to all other members.
func (dm *DistMemory) startAutoSyncIfEnabled(ctx context.Context) {
	if dm.autoSyncInterval <= 0 || dm.membership == nil {
		return
	}

	if dm.autoSyncStopCh != nil { // already running
		return
	}

	stopCh := make(chan struct{})

	dm.autoSyncStopCh = stopCh

	interval := dm.autoSyncInterval
	go dm.autoSyncLoop(ctx, interval, stopCh)

	dm.logger.Info(
		"merkle auto-sync loop started",
		slog.Duration("interval", interval),
		slog.Int("peer_cap_per_tick", dm.autoSyncPeersPerInterval),
	)
}

// syncWithStatus mirrors SyncWith but additionally reports whether the cycle
// found no divergence (clean=true means: zero differing chunks, zero
// remote-only keys). Used by runAutoSyncTick to drive the adaptive backoff
// without changing the public SyncWith signature.
func (dm *DistMemory) syncWithStatus(ctx context.Context, nodeID string) (bool, error) {
	transport := dm.loadTransport()
	if transport == nil {
		return false, errNoTransport
	}

	startFetch := time.Now()

	remoteTree, err := transport.FetchMerkle(ctx, nodeID)
	if err != nil {
		dm.logger.Warn(
			"merkle sync fetch failed",
			slog.String("peer_id", nodeID),
			slog.Any("err", err),
		)

		return false, err
	}

	fetchDur := time.Since(startFetch)
	dm.metrics.merkleFetchNanos.Store(fetchDur.Nanoseconds())

	startBuild := time.Now()
	localTree := dm.BuildMerkleTree()
	buildDur := time.Since(startBuild)
	dm.metrics.merkleBuildNanos.Store(buildDur.Nanoseconds())

	entries := dm.sortedMerkleEntries()
	startDiff := time.Now()
	diffs := localTree.DiffLeafRanges(remoteTree)
	diffDur := time.Since(startDiff)
	dm.metrics.merkleDiffNanos.Store(diffDur.Nanoseconds())

	missing := dm.resolveMissingKeys(ctx, nodeID, entries)

	dm.applyMerkleDiffs(ctx, nodeID, entries, diffs, localTree.ChunkSize)

	for k := range missing { // missing = remote-only keys
		dm.fetchAndAdopt(ctx, nodeID, k)
	}

	if len(diffs) == 0 && len(missing) == 0 {
		return true, nil
	}

	dm.metrics.merkleSyncs.Add(1)

	return false, nil
}

// autoSyncLoop drives Merkle anti-entropy on a timer. With adaptive
// backoff disabled (the default) it behaves like a fixed-interval ticker.
// With backoff enabled it resets the timer between ticks using
// nextAutoSyncDelay, doubling on clean cycles and snapping back to 1× on
// dirty ones.
func (dm *DistMemory) autoSyncLoop(ctx context.Context, interval time.Duration, stopCh <-chan struct{}) {
	if dm.autoSyncMaxBackoffFactor > 1 {
		dm.autoSyncBackoffFactor.Store(1)
	}

	timer := time.NewTimer(interval)
	defer timer.Stop()

	for {
		select {
		case <-stopCh:
			return
		case <-timer.C:
			clean := dm.runAutoSyncTick(ctx)
			dm.updateAutoSyncBackoff(clean)
			timer.Reset(dm.nextAutoSyncDelay(interval))
		}
	}
}

// runAutoSyncTick performs one auto-sync cycle. Returns true when every
// peer reported a clean (no-divergence) result and at least one peer was
// actually synced; false on any divergence, any sync error, or when no
// peers were eligible (caller treats "no peers" as not-clean to avoid
// backing off in single-node configurations).
func (dm *DistMemory) runAutoSyncTick(ctx context.Context) bool {
	start := time.Now()

	var lastErr error

	members := dm.membership.List()
	limit := dm.autoSyncPeersPerInterval
	synced := 0
	dirty := false

	for _, member := range members {
		if member == nil || string(member.ID) == dm.nodeID { // skip self
			continue
		}

		if limit > 0 && synced >= limit {
			break
		}

		clean, err := dm.syncWithStatus(ctx, string(member.ID))
		if err != nil { // capture last error only; treat as dirty so we don't back off through outages
			lastErr = err
			dirty = true
		} else if !clean {
			dirty = true
		}

		synced++
	}

	dm.lastAutoSyncDuration.Store(time.Since(start).Nanoseconds())

	if lastErr != nil {
		dm.lastAutoSyncError.Store(lastErr.Error())
	} else {
		dm.lastAutoSyncError.Store("")
	}

	dm.metrics.autoSyncLoops.Add(1)

	return synced > 0 && !dirty
}

// updateAutoSyncBackoff advances the backoff factor based on tick outcome.
// Clean tick: double up to the cap. Dirty tick: snap back to 1×. No-op
// when adaptive backoff is disabled (autoSyncMaxBackoffFactor <= 1).
func (dm *DistMemory) updateAutoSyncBackoff(clean bool) {
	if dm.autoSyncMaxBackoffFactor <= 1 {
		return
	}

	prev := dm.autoSyncBackoffFactor.Load()

	var next int64

	if clean {
		dm.autoSyncCleanTicks.Add(1)

		next = min(prev*2, int64(dm.autoSyncMaxBackoffFactor))
	} else {
		next = 1
	}

	if next == prev {
		return
	}

	dm.autoSyncBackoffFactor.Store(next)
	dm.logger.Info(
		"merkle auto-sync backoff factor changed",
		slog.Int64("from", prev),
		slog.Int64("to", next),
		slog.Bool("clean", clean),
	)
}

// nextAutoSyncDelay returns the sleep duration before the next auto-sync
// tick. When adaptive backoff is disabled this is just `interval`; when
// enabled it is `interval * currentFactor`.
func (dm *DistMemory) nextAutoSyncDelay(interval time.Duration) time.Duration {
	if dm.autoSyncMaxBackoffFactor <= 1 {
		return interval
	}

	factor := max(dm.autoSyncBackoffFactor.Load(), 1)

	return interval * time.Duration(factor)
}

func (dm *DistMemory) gossipLoop(stopCh <-chan struct{}) {
	ticker := time.NewTicker(dm.gossipInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			dm.runGossipTick()
		case <-stopCh:
			return
		}
	}
}

func (dm *DistMemory) runGossipTick() {
	if dm.membership == nil || dm.loadTransport() == nil {
		return
	}

	peers := dm.membership.List()
	if len(peers) <= 1 {
		return
	}

	var candidates []*cluster.Node

	for _, n := range peers {
		if n.ID != dm.localNode.ID {
			candidates = append(candidates, n)
		}
	}

	if len(candidates) == 0 {
		return
	}

	// secure random selection (not strictly required but avoids G404 warning)
	n := len(candidates)

	idxBig, err := rand.Int(rand.Reader, big.NewInt(int64(n))) // #nosec G404 - cryptographic randomness acceptable here
	if err != nil {
		return
	}

	target := candidates[idxBig.Int64()]
	transport := dm.loadTransport()
	snapshot := dm.membership.List()

	// In-process fast path: skip the wire and call acceptGossip
	// directly. Pre-Phase-E this was the ONLY path; the function
	// bailed for any other transport type, so cross-process
	// clusters never disseminated membership / never refuted
	// suspect claims. The fall-through below now uses the
	// transport's Gossip method, which routes via HTTP for the
	// auto-created DistHTTPTransport.
	if ip, ok := transport.(*InProcessTransport); ok {
		if remote, ok2 := ip.backends[string(target.ID)]; ok2 {
			remote.acceptGossip(snapshot)
		}

		return
	}

	gossipErr := transport.Gossip(dm.lifeCtx, string(target.ID), nodesToGossipMembers(snapshot))
	if gossipErr != nil {
		dm.logger.Debug(
			"gossip push failed",
			slog.String("peer_id", string(target.ID)),
			slog.Any("err", gossipErr),
		)
	}
}

func (dm *DistMemory) acceptGossip(nodes []*cluster.Node) {
	if dm.membership == nil {
		return
	}

	for _, node := range nodes {
		if node.ID == dm.localNode.ID {
			dm.refuteIfSuspected(node)

			continue
		}

		existing := false
		for _, cur := range dm.membership.List() {
			if cur.ID == node.ID {
				existing = true

				if node.Incarnation > cur.Incarnation {
					dm.membership.Upsert(&cluster.Node{
						ID:          node.ID,
						Address:     node.Address,
						State:       node.State,
						Incarnation: node.Incarnation,
						LastSeen:    time.Now(),
					})
				}

				break
			}
		}

		if !existing {
			dm.membership.Upsert(&cluster.Node{
				ID:          node.ID,
				Address:     node.Address,
				State:       node.State,
				Incarnation: node.Incarnation,
				LastSeen:    time.Now(),
			})
		}
	}
}

// refuteIfSuspected handles the SWIM self-refute path: when a peer
// gossips that THIS node is Suspect or Dead at incarnation N, bump
// our local incarnation to N+1 and re-upsert ourselves as Alive.
// Higher-incarnation-wins propagation in `acceptGossip` ensures the
// next gossip tick disseminates the refutation cluster-wide.
//
// Pre-fix this path was a no-op (`continue` on local-ID match) — a
// node that fell briefly behind heartbeat would be marked Suspect by
// peers and could not undo it through gossip; only a fresh probe
// would clear suspicion. Self-refute closes the loop required for
// the heartbeat marker to drop its `experimental` qualifier.
func (dm *DistMemory) refuteIfSuspected(claim *cluster.Node) {
	if claim == nil || dm.localNode == nil {
		return
	}

	if claim.State == cluster.NodeAlive {
		return // peer agrees we're alive — nothing to refute
	}

	// Only refute when the peer's claim is at >= our incarnation;
	// older claims are stale and ignored.
	if claim.Incarnation < dm.localNode.Incarnation {
		return
	}

	dm.membership.Mark(dm.localNode.ID, cluster.NodeAlive)

	dm.logger.Info(
		"self-refuted suspect/dead claim from peer",
		slog.Uint64("claim_incarnation", claim.Incarnation),
		slog.String("claim_state", claim.State.String()),
	)
}

// chooseNewer picks the item with higher version; on version tie uses lexicographically smaller Origin as winner.
func (dm *DistMemory) chooseNewer(itemA, itemB *cache.Item) *cache.Item {
	if itemA == nil {
		return itemB
	}

	if itemB == nil {
		return itemA
	}

	if itemB.Version > itemA.Version { // itemB newer
		dm.metrics.versionConflicts.Add(1)

		return itemB
	}

	if itemA.Version > itemB.Version { // itemA newer
		dm.metrics.versionConflicts.Add(1)

		return itemA
	}

	// versions equal: tie-break on origin
	if itemB.Origin < itemA.Origin { // itemB wins by tie-break
		dm.metrics.versionConflicts.Add(1)
		dm.metrics.versionTieBreaks.Add(1)

		return itemB
	}

	if itemA.Origin < itemB.Origin { // itemA wins by tie-break (still counts)
		dm.metrics.versionConflicts.Add(1)
		dm.metrics.versionTieBreaks.Add(1)
	}

	return itemA
}

// repairReplicas ensures each owner has at least the chosen version; best-effort.
func (dm *DistMemory) repairReplicas(ctx context.Context, key string, chosen *cache.Item, owners []cluster.NodeID) {
	if chosen == nil {
		return
	}

	for _, oid := range owners {
		if oid == dm.localNode.ID {
			dm.repairLocalReplica(ctx, key, chosen)

			continue
		}

		dm.repairRemoteReplica(ctx, key, chosen, oid)
	}
}

// repairLocalReplica updates the local item if stale.
func (dm *DistMemory) repairLocalReplica(ctx context.Context, key string, chosen *cache.Item) { // separated to reduce cyclomatic complexity
	localIt, ok := dm.shardFor(key).items.Get(key)
	if !ok || localIt.Version < chosen.Version || (localIt.Version == chosen.Version && localIt.Origin > chosen.Origin) {
		cloned := *chosen
		dm.applySet(ctx, &cloned, false)
		dm.metrics.readRepair.Add(1)
	}
}

// repairRemoteReplica dispatches a stale-replica repair to a remote owner.
//
// We send the chosen item unconditionally — no probe-then-write dance — because
// the receiver's applySet already version-compares and noops a write older or
// equal to what it holds. The previous shape did a defensive ForwardGet first;
// dropping it cuts every repair from two transport calls to one, with the
// receiver's version logic providing the same staleness gate it always did.
//
// If WithDistReadRepairBatch was set, the repair is enqueued for coalescing
// instead of dispatching synchronously; the read path returns without waiting
// for the wire call. The async window is bounded by the configured interval
// and drained on Stop.
func (dm *DistMemory) repairRemoteReplica(
	ctx context.Context,
	_ string,
	chosen *cache.Item,
	oid cluster.NodeID,
) {
	transport := dm.loadTransport()
	if transport == nil { // cannot repair remote
		return
	}

	if dm.repairQueue != nil {
		dm.repairQueue.enqueue(ctx, oid, chosen)
		dm.metrics.readRepair.Add(1)

		return
	}

	_ = transport.ForwardSet(ctx, string(oid), chosen, false)

	dm.metrics.readRepair.Add(1)
}

// handleForwardPrimary tries to forward a Set to the primary; returns (proceedAsPrimary,false) if promotion required.
func (dm *DistMemory) handleForwardPrimary(ctx context.Context, owners []cluster.NodeID, item *cache.Item) (bool, error) {
	transport := dm.loadTransport()
	if transport == nil {
		return false, sentinel.ErrNotOwner
	}

	dm.metrics.forwardSet.Add(1)

	errFwd := transport.ForwardSet(ctx, string(owners[0]), item, true)
	switch {
	case errFwd == nil:
		return false, nil // forwarded successfully
	case errors.Is(errFwd, sentinel.ErrBackendNotFound) && len(owners) > 1:
		// primary missing: promote if this node is a listed replica
		for _, oid := range owners[1:] {
			if oid == dm.localNode.ID { // we can promote
				if !dm.ownsKeyInternal(item.Key) { // still not recognized locally (ring maybe outdated)
					return false, errFwd
				}

				return true, nil // proceed as primary path
			}
		}

		return false, errFwd // not promotable

	default:
		return false, errFwd
	}
}

// initStandaloneMembership initializes membership & ring for standalone mode with optional seeds.
func (dm *DistMemory) initStandaloneMembership() {
	ringOpts := []cluster.RingOption{}
	if dm.replication > 0 {
		ringOpts = append(ringOpts, cluster.WithReplication(dm.replication))
	}

	if dm.virtualNodes > 0 {
		ringOpts = append(ringOpts, cluster.WithVirtualNodes(dm.virtualNodes))
	}

	ring := cluster.NewRing(ringOpts...)
	membership := cluster.NewMembership(ring)

	if dm.localNode == nil {
		addr := dm.nodeAddr
		if addr == "" {
			addr = "local"
		}

		dm.localNode = cluster.NewNode(dm.nodeID, addr)
	}

	membership.Upsert(dm.localNode)

	for _, raw := range dm.seeds { // add seeds
		spec := parseSeedSpec(raw)
		if spec.addr == dm.localNode.Address { // skip self
			continue
		}

		if spec.id == string(dm.localNode.ID) { // skip self by ID too
			continue
		}

		membership.Upsert(cluster.NewNode(spec.id, spec.addr))
	}

	dm.membership = membership
	dm.ring = ring
}

// seedSpec carries a parsed seed entry. Returned as a struct (not two
// strings) so the same-typed pair doesn't trip the confusing-results
// linter while staying compatible with the no-named-returns rule.
type seedSpec struct {
	id   string
	addr string
}

// parseSeedSpec splits a seed entry into id + addr. The accepted
// shapes are `id@addr` (cross-process clusters where every node
// must know its peers' IDs to route through the consistent hash
// ring) and bare `addr` (legacy / in-process tests that rely on
// heartbeat or gossip to fill the ID later). Everything before the
// first `@` is the ID; everything after is the address.
//
// `id@addr` is what the production server binary uses — without IDs
// in seeds, the ring lookups return empty owners, every node
// promotes itself, and writes never propagate across the cluster.
func parseSeedSpec(raw string) seedSpec {
	id, addr, found := strings.Cut(raw, "@")
	if !found {
		return seedSpec{addr: raw}
	}

	return seedSpec{id: id, addr: addr}
}

// heartbeatLoop probes peers and updates membership. SWIM-style
// indirect probes (Phase B.1) and self-refutation via gossip
// (Phase E) are wired into the surrounding helpers — this loop
// only schedules the per-tick work.
func (dm *DistMemory) heartbeatLoop(ctx context.Context, stopCh <-chan struct{}) { // reduced cognitive complexity via helpers
	ticker := time.NewTicker(dm.hbInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			dm.runHeartbeatTick(ctx)
		case <-stopCh:
			return
		}
	}
}

// --- Internal helpers (after exported methods) ---

// hashKey returns shard index for key.
func (dm *DistMemory) hashKey(key string) int {
	h := fnv.New32a()

	_, _ = h.Write([]byte(key)) // hash write cannot error per docs

	return int(h.Sum32()) % dm.shardCount
}

func (dm *DistMemory) shardFor(key string) *distShard { return dm.shards[dm.hashKey(key)] }

// isOwner returns true if this instance is among ring owners (primary or replica) for key.
func (dm *DistMemory) ownsKeyInternal(key string) bool {
	if dm.ring == nil { // treat nil ring as local-only mode
		return true
	}

	owners := dm.ring.Lookup(key)
	if slices.Contains(owners, dm.localNode.ID) {
		return true
	}

	return len(owners) == 0 // empty ring => owner
}

// getImpl is the business logic for Get, separated so the public Get
// stays a thin tracing/latency wrapper. The split keeps the tracing
// wrapper to a literal `defer span.End()`, avoids named returns, and
// still lets the wrapper set `cache.hit` after the impl returns.
func (dm *DistMemory) getImpl(ctx context.Context, key string) (*cache.Item, bool) {
	if dm.readConsistency == ConsistencyOne { // fast local path
		if it, ok := dm.shardFor(key).items.GetCopy(key); ok {
			return it, true
		}
	}

	owners := dm.lookupOwners(key)
	if len(owners) == 0 {
		return nil, false
	}

	if dm.readConsistency == ConsistencyOne {
		return dm.getOne(ctx, key, owners)
	}

	if dm.parallelReads {
		return dm.getWithConsistencyParallel(ctx, key, owners)
	}

	return dm.getWithConsistency(ctx, key, owners)
}

// setImpl is the business logic for Set. Takes the active span as a
// param so it can attach owners.count / acks attributes mid-flight;
// returns the operation error for the wrapper to record on the span.
func (dm *DistMemory) setImpl(ctx context.Context, item *cache.Item, span trace.Span) error {
	if dm.draining.Load() {
		return sentinel.ErrDraining
	}

	dm.metrics.writeAttempts.Add(1)

	owners := dm.lookupOwners(item.Key)
	if len(owners) == 0 {
		return sentinel.ErrNotOwner
	}

	span.SetAttributes(attribute.Int("dist.owners.count", len(owners)))

	if owners[0] != dm.localNode.ID { // attempt forward; may promote
		proceedAsPrimary, ferr := dm.handleForwardPrimary(ctx, owners, item)
		if ferr != nil {
			return ferr
		}

		if !proceedAsPrimary { // forwarded successfully; nothing else to do
			return nil
		}
	}

	// primary path: assign version & timestamp
	item.Version = dm.versionCounter.Add(1)
	item.Origin = string(dm.localNode.ID)
	item.LastUpdated = time.Now()
	dm.applySet(ctx, item, false)

	acks := 1 + dm.replicateTo(ctx, item, owners[1:])
	dm.metrics.writeAcks.Add(int64(acks))

	span.SetAttributes(attribute.Int("dist.acks", acks))

	needed := dm.requiredAcks(len(owners), dm.writeConsistency)
	if acks < needed {
		dm.metrics.writeQuorumFailures.Add(1)

		return sentinel.ErrQuorumFailed
	}

	return nil
}

// removeImpl is the business logic for Remove. Mirrors setImpl's
// primary-routing semantics: only owners[0] runs applyRemove +
// replica fan-out locally; everyone else (replicas, non-owners)
// forwards to the primary so the delete reaches every owner.
//
// Pre-fix this branched on dm.ownsKeyInternal which returns true for
// any owner — replica-initiated removes ran applyRemove locally,
// whose fan-out then skipped owners[0] under the assumption that
// the caller WAS owners[0]. Net effect: deletes from a replica
// never reached the primary, and the value lingered until TTL.
func (dm *DistMemory) removeImpl(ctx context.Context, keys []string) error {
	if dm.draining.Load() {
		return sentinel.ErrDraining
	}

	for _, key := range keys {
		owners := dm.lookupOwners(key)
		if len(owners) == 0 {
			continue
		}

		if owners[0] == dm.localNode.ID { // primary path
			dm.applyRemove(ctx, key, true)

			continue
		}

		transport := dm.loadTransport()
		if transport == nil { // non-primary without transport
			return sentinel.ErrNotOwner
		}

		dm.metrics.forwardRemove.Add(1)

		_ = transport.ForwardRemove(ctx, string(owners[0]), key, true)
	}

	return nil
}

// distMetricSpec describes one OTel observable instrument backed by a
// field on DistMetrics. The kind selects between cumulative-counter and
// gauge semantics (OTel exporters render them differently); `get` reads
// the value from a DistMetrics snapshot.
type distMetricSpec struct {
	name    string
	desc    string
	unit    string
	counter bool // true = ObservableCounter, false = ObservableGauge
	get     func(DistMetrics) int64
}

// OTel UCUM-compatible unit annotations for the dist metrics. Pulled
// out as constants so adjacent table entries don't repeat the literal
// (and trip the goconst linter).
const (
	unitOp         = "{op}"
	unitProbe      = "{probe}"
	unitTransition = "{transition}"
	unitResolution = "{resolution}"
	unitHint       = "{hint}"
	unitBytes      = "By"
	unitSync       = "{sync}"
	unitKey        = "{key}"
	unitTick       = "{tick}"
	unitTombstone  = "{tombstone}"
	unitAck        = "{ack}"
	unitBatch      = "{batch}"
	unitEvent      = "{event}"
	unitNanos      = "ns"
	unitNode       = "{node}"
	unitVersion    = "{version}"
)

// distMetricSpecs is the registry of every DistMetrics field exposed
// via OpenTelemetry. Names are dot-separated and `dist.`-prefixed so a
// Prometheus exporter renders them as `dist_<metric>` under a single
// subsystem. New atomics on distMetrics MUST be mirrored here — the
// JSON snapshot at /dist/metrics is the human-facing surface, OTel is
// the production-pipeline surface, and divergence between them creates
// confusing operator handoffs.
//
//nolint:gochecknoglobals // package-level table is the registration source of truth; alternatives hurt readability
var distMetricSpecs = []distMetricSpec{
	// --- Routing / forwarding (cumulative) ---
	{
		name: "dist.forward.get", unit: unitOp, counter: true,
		desc: "Get requests forwarded to a remote owner",
		get:  func(m DistMetrics) int64 { return m.ForwardGet },
	},
	{
		name: "dist.forward.set", unit: unitOp, counter: true,
		desc: "Set requests forwarded to a remote primary",
		get:  func(m DistMetrics) int64 { return m.ForwardSet },
	},
	{
		name: "dist.forward.remove", unit: unitOp, counter: true,
		desc: "Remove requests forwarded to a remote primary",
		get:  func(m DistMetrics) int64 { return m.ForwardRemove },
	},
	{
		name: "dist.replica.fanout.set", unit: unitOp, counter: true,
		desc: "Replica fan-out attempts on Set",
		get:  func(m DistMetrics) int64 { return m.ReplicaFanoutSet },
	},
	{
		name: "dist.replica.fanout.remove", unit: unitOp, counter: true,
		desc: "Replica fan-out attempts on Remove",
		get:  func(m DistMetrics) int64 { return m.ReplicaFanoutRemove },
	},
	{
		name: "dist.read.repair", unit: unitOp, counter: true,
		desc: "Read-repair operations triggered by stale-replica reads",
		get:  func(m DistMetrics) int64 { return m.ReadRepair },
	},
	{
		name: "dist.read.repair.batched", unit: unitOp, counter: true,
		desc: "Read-repair operations dispatched via the async coalescer (subset of dist.read.repair)",
		get:  func(m DistMetrics) int64 { return m.ReadRepairBatched },
	},
	{
		name: "dist.read.repair.coalesced", unit: unitOp, counter: true,
		desc: "Read-repair operations short-circuited by the coalescer because a queued entry already covered the (peer, key)",
		get:  func(m DistMetrics) int64 { return m.ReadRepairCoalesced },
	},
	{
		name: "dist.replica.get.miss", unit: unitOp, counter: true,
		desc: "Replica Get returned not-found for a key the primary holds",
		get:  func(m DistMetrics) int64 { return m.ReplicaGetMiss },
	},
	{
		name: "dist.read.primary_promote", unit: unitOp, counter: true,
		desc: "Read path promoted to next owner after primary unreachable",
		get:  func(m DistMetrics) int64 { return m.ReadPrimaryPromote },
	},

	// --- Heartbeat / membership transitions (cumulative) ---
	{
		name: "dist.heartbeat.success", unit: unitProbe, counter: true,
		desc: "Successful heartbeat probes",
		get:  func(m DistMetrics) int64 { return m.HeartbeatSuccess },
	},
	{
		name: "dist.heartbeat.failure", unit: unitProbe, counter: true,
		desc: "Failed heartbeat probes",
		get:  func(m DistMetrics) int64 { return m.HeartbeatFailure },
	},
	{
		name: "dist.heartbeat.indirect_probe.success", unit: unitProbe, counter: true,
		desc: "Indirect probes that succeeded (relay reached target)",
		get:  func(m DistMetrics) int64 { return m.IndirectProbeSuccess },
	},
	{
		name: "dist.heartbeat.indirect_probe.failure", unit: unitProbe, counter: true,
		desc: "Indirect probes that failed (relay confirmed target unreachable)",
		get:  func(m DistMetrics) int64 { return m.IndirectProbeFailure },
	},
	{
		name: "dist.heartbeat.indirect_probe.refuted", unit: unitProbe, counter: true,
		desc: "Direct probe failed but indirect probe succeeded — caller-side network was the issue",
		get:  func(m DistMetrics) int64 { return m.IndirectProbeRefuted },
	},
	{
		name: "dist.drains", unit: unitTransition, counter: true,
		desc: "Drain transitions observed on this node (cumulative; 0 or 1 in normal use)",
		get:  func(m DistMetrics) int64 { return m.Drains },
	},
	{
		name: "dist.nodes.suspect", unit: unitTransition, counter: true,
		desc: "Cumulative peer transitions to suspect state",
		get:  func(m DistMetrics) int64 { return m.NodesSuspect },
	},
	{
		name: "dist.nodes.dead", unit: unitTransition, counter: true,
		desc: "Cumulative peer transitions to dead state",
		get:  func(m DistMetrics) int64 { return m.NodesDead },
	},
	{
		name: "dist.nodes.removed", unit: unitTransition, counter: true,
		desc: "Cumulative peer prunes from membership",
		get:  func(m DistMetrics) int64 { return m.NodesRemoved },
	},

	// --- Versioning (cumulative) ---
	{
		name: "dist.version.conflicts", unit: unitResolution, counter: true,
		desc: "Version-conflict resolutions (later version replaced earlier)",
		get:  func(m DistMetrics) int64 { return m.VersionConflicts },
	},
	{
		name: "dist.version.tie_breaks", unit: unitResolution, counter: true,
		desc: "Version-conflict tie-breaks decided by origin",
		get:  func(m DistMetrics) int64 { return m.VersionTieBreaks },
	},

	// --- Hinted handoff (cumulative + bytes gauge) ---
	{
		name: "dist.hinted.queued", unit: unitHint, counter: true,
		desc: "Hints queued for unreachable peers",
		get:  func(m DistMetrics) int64 { return m.HintedQueued },
	},
	{
		name: "dist.hinted.replayed", unit: unitHint, counter: true,
		desc: "Hints successfully replayed",
		get:  func(m DistMetrics) int64 { return m.HintedReplayed },
	},
	{
		name: "dist.hinted.expired", unit: unitHint, counter: true,
		desc: "Hints expired before delivery",
		get:  func(m DistMetrics) int64 { return m.HintedExpired },
	},
	{
		name: "dist.hinted.dropped", unit: unitHint, counter: true,
		desc: "Hints dropped after replay error (non-recoverable)",
		get:  func(m DistMetrics) int64 { return m.HintedDropped },
	},
	{
		name: "dist.hinted.global_dropped", unit: unitHint, counter: true,
		desc: "Hints dropped due to global queue caps (count/bytes)",
		get:  func(m DistMetrics) int64 { return m.HintedGlobalDropped },
	},
	{
		name: "dist.hinted.bytes", unit: unitBytes, counter: false,
		desc: "Approximate total bytes currently queued in hints",
		get:  func(m DistMetrics) int64 { return m.HintedBytes },
	},
	{
		name: "dist.migration.queued", unit: unitHint, counter: true,
		desc: "Migration-source hints queued (subset of dist.hinted.queued from rebalance ticks)",
		get:  func(m DistMetrics) int64 { return m.MigrationHintQueued },
	},
	{
		name: "dist.migration.replayed", unit: unitHint, counter: true,
		desc: "Migration-source hints successfully delivered on replay",
		get:  func(m DistMetrics) int64 { return m.MigrationHintReplayed },
	},
	{
		name: "dist.migration.expired", unit: unitHint, counter: true,
		desc: "Migration-source hints that aged past hint TTL before delivery",
		get:  func(m DistMetrics) int64 { return m.MigrationHintExpired },
	},
	{
		name: "dist.migration.dropped", unit: unitHint, counter: true,
		desc: "Migration-source hints discarded by replay error or queue caps",
		get:  func(m DistMetrics) int64 { return m.MigrationHintDropped },
	},
	{
		name: "dist.migration.last_age_ns", unit: unitNanos, counter: false,
		desc: "Queue residency of the most-recently-replayed migration hint (ns)",
		get:  func(m DistMetrics) int64 { return m.MigrationHintLastAgeNanos },
	},

	// --- Anti-entropy (Merkle) ---
	{
		name: "dist.merkle.syncs", unit: unitSync, counter: true,
		desc: "Completed Merkle sync operations",
		get:  func(m DistMetrics) int64 { return m.MerkleSyncs },
	},
	{
		name: "dist.merkle.keys_pulled", unit: unitKey, counter: true,
		desc: "Keys applied during Merkle sync",
		get:  func(m DistMetrics) int64 { return m.MerkleKeysPulled },
	},
	{
		name: "dist.merkle.last_build_ns", unit: unitNanos, counter: false,
		desc: "Duration of last Merkle tree build",
		get:  func(m DistMetrics) int64 { return m.MerkleBuildNanos },
	},
	{
		name: "dist.merkle.last_diff_ns", unit: unitNanos, counter: false,
		desc: "Duration of last Merkle diff computation",
		get:  func(m DistMetrics) int64 { return m.MerkleDiffNanos },
	},
	{
		name: "dist.merkle.last_fetch_ns", unit: unitNanos, counter: false,
		desc: "Duration of last remote Merkle tree fetch",
		get:  func(m DistMetrics) int64 { return m.MerkleFetchNanos },
	},
	{
		name: "dist.auto_sync.loops", unit: unitTick, counter: true,
		desc: "Auto-sync ticks executed",
		get:  func(m DistMetrics) int64 { return m.AutoSyncLoops },
	},
	{
		name: "dist.auto_sync.last_ns", unit: unitNanos, counter: false,
		desc: "Duration of last auto-sync loop",
		get:  func(m DistMetrics) int64 { return m.LastAutoSyncNanos },
	},
	{
		name: "dist.auto_sync.clean_ticks", unit: unitTick, counter: true,
		desc: "Auto-sync ticks where every peer returned zero divergence (drives adaptive backoff)",
		get:  func(m DistMetrics) int64 { return m.AutoSyncCleanTicks },
	},
	{
		name: "dist.auto_sync.backoff_factor", unit: unitTick, counter: false,
		desc: "Current adaptive auto-sync backoff multiplier (1 when disabled or reset)",
		get:  func(m DistMetrics) int64 { return m.AutoSyncBackoffFactor },
	},
	{
		name: "dist.chaos.drops", unit: unitEvent, counter: true,
		desc: "Transport calls dropped by configured Chaos (test-only; zero in production)",
		get:  func(m DistMetrics) int64 { return m.ChaosDrops },
	},
	{
		name: "dist.chaos.latencies", unit: unitEvent, counter: true,
		desc: "Transport calls that had latency injected by Chaos (test-only; zero in production)",
		get:  func(m DistMetrics) int64 { return m.ChaosLatencies },
	},

	// --- Tombstones ---
	{
		name: "dist.tombstones.active", unit: unitTombstone, counter: false,
		desc: "Approximate active tombstones across all shards",
		get:  func(m DistMetrics) int64 { return m.TombstonesActive },
	},
	{
		name: "dist.tombstones.purged", unit: unitTombstone, counter: true,
		desc: "Cumulative tombstones purged by sweeper",
		get:  func(m DistMetrics) int64 { return m.TombstonesPurged },
	},

	// --- Writes / quorum ---
	{
		name: "dist.write.attempts", unit: unitOp, counter: true,
		desc: "Total Set operations attempted",
		get:  func(m DistMetrics) int64 { return m.WriteAttempts },
	},
	{
		name: "dist.write.acks", unit: unitAck, counter: true,
		desc: "Cumulative Set replica acks (includes primary)",
		get:  func(m DistMetrics) int64 { return m.WriteAcks },
	},
	{
		name: "dist.write.quorum_failures", unit: unitOp, counter: true,
		desc: "Set operations that failed quorum",
		get:  func(m DistMetrics) int64 { return m.WriteQuorumFailures },
	},

	// --- Rebalance ---
	{
		name: "dist.rebalance.keys", unit: unitKey, counter: true,
		desc: "Keys migrated during rebalance",
		get:  func(m DistMetrics) int64 { return m.RebalancedKeys },
	},
	{
		name: "dist.rebalance.batches", unit: unitBatch, counter: true,
		desc: "Rebalance batches processed",
		get:  func(m DistMetrics) int64 { return m.RebalanceBatches },
	},
	{
		name: "dist.rebalance.throttle", unit: unitEvent, counter: true,
		desc: "Rebalance throttle events (concurrency cap reached)",
		get:  func(m DistMetrics) int64 { return m.RebalanceThrottle },
	},
	{
		name: "dist.rebalance.last_ns", unit: unitNanos, counter: false,
		desc: "Duration of last rebalance scan",
		get:  func(m DistMetrics) int64 { return m.RebalanceLastNanos },
	},
	{
		name: "dist.rebalance.replica_diff", unit: unitKey, counter: true,
		desc: "Keys pushed to newly added replicas (replica-only diff)",
		get:  func(m DistMetrics) int64 { return m.RebalancedReplicaDiff },
	},
	{
		name: "dist.rebalance.replica_diff_throttle", unit: unitEvent, counter: true,
		desc: "Replica-diff scans that exited early due to per-tick limit",
		get:  func(m DistMetrics) int64 { return m.RebalanceReplicaDiffThrottle },
	},
	{
		name: "dist.rebalance.primary", unit: unitKey, counter: true,
		desc: "Keys whose primary ownership changed",
		get:  func(m DistMetrics) int64 { return m.RebalancedPrimary },
	},

	// --- Membership state (gauges) ---
	{
		name: "dist.members.alive", unit: unitNode, counter: false,
		desc: "Currently alive members in the cluster",
		get:  func(m DistMetrics) int64 { return m.MembersAlive },
	},
	{
		name: "dist.members.suspect", unit: unitNode, counter: false,
		desc: "Currently suspect members in the cluster",
		get:  func(m DistMetrics) int64 { return m.MembersSuspect },
	},
	{
		name: "dist.members.dead", unit: unitNode, counter: false,
		desc: "Currently dead members in the cluster",
		get:  func(m DistMetrics) int64 { return m.MembersDead },
	},
	{
		name: "dist.membership.version", unit: unitVersion, counter: false,
		desc: "Membership version (monotonic, increments on changes)",
		get:  func(m DistMetrics) int64 { return int64(m.MembershipVersion) },
	},
}

// installTelemetryDefaults wires the no-op fallbacks for logger,
// tracer, and meter so every call site downstream can use them without
// nil-checks; binds node identity onto the logger so call sites don't
// have to re-attach it on every record; and registers the OTel metric
// callback. Extracted from NewDistMemory to keep that constructor
// under the function-length lint cap.
func (dm *DistMemory) installTelemetryDefaults() {
	if dm.logger == nil {
		dm.logger = slog.New(slog.DiscardHandler)
	}

	dm.logger = dm.logger.With(
		slog.String("component", "dist_memory"),
		slog.String("node_id", dm.nodeID),
	)

	if dm.tracer == nil {
		dm.tracer = noop.NewTracerProvider().Tracer(distTracerName)
	}

	if dm.meter == nil {
		dm.meter = metricnoop.NewMeterProvider().Meter(distTracerName)
	}

	dm.setupOTelMetrics()
}

// distMetricInstruments holds the OTel observable instruments created
// for each distMetricSpec. Indexed by spec position so the callback
// can pair each spec back with its instrument; counter/gauge maps are
// disjoint.
type distMetricInstruments struct {
	counters    map[int]metric.Int64ObservableCounter
	gauges      map[int]metric.Int64ObservableGauge
	instruments []metric.Observable
}

// createInstrument turns one spec into the appropriate observable
// instrument and stashes it on `inst`. Errors are logged and the spec
// is skipped — registration glue must never abort cache startup.
func (dm *DistMemory) createInstrument(idx int, spec distMetricSpec, inst *distMetricInstruments) {
	if spec.counter {
		counter, err := dm.meter.Int64ObservableCounter(spec.name,
			metric.WithDescription(spec.desc), metric.WithUnit(spec.unit))
		if err != nil {
			dm.logger.Error("dist meter: counter registration failed",
				slog.String("metric", spec.name), slog.Any("err", err))

			return
		}

		inst.counters[idx] = counter
		inst.instruments = append(inst.instruments, counter)

		return
	}

	gauge, err := dm.meter.Int64ObservableGauge(spec.name,
		metric.WithDescription(spec.desc), metric.WithUnit(spec.unit))
	if err != nil {
		dm.logger.Error("dist meter: gauge registration failed",
			slog.String("metric", spec.name), slog.Any("err", err))

		return
	}

	inst.gauges[idx] = gauge
	inst.instruments = append(inst.instruments, gauge)
}

// setupOTelMetrics registers every distMetricSpec as an observable
// instrument and wires a single callback that observes all of them
// from one Metrics() snapshot per collection cycle. Failures are
// logged (not returned) because metric registration is library-side
// glue: the cache must remain functional even if the meter pipeline
// rejects an instrument.
func (dm *DistMemory) setupOTelMetrics() {
	inst := &distMetricInstruments{
		counters:    make(map[int]metric.Int64ObservableCounter, len(distMetricSpecs)),
		gauges:      make(map[int]metric.Int64ObservableGauge, len(distMetricSpecs)),
		instruments: make([]metric.Observable, 0, len(distMetricSpecs)),
	}

	for i, spec := range distMetricSpecs {
		dm.createInstrument(i, spec, inst)
	}

	if len(inst.instruments) == 0 {
		return
	}

	reg, regErr := dm.meter.RegisterCallback(
		func(_ context.Context, observer metric.Observer) error {
			snapshot := dm.Metrics()

			for i, spec := range distMetricSpecs {
				if c, ok := inst.counters[i]; ok {
					observer.ObserveInt64(c, spec.get(snapshot))

					continue
				}

				if g, ok := inst.gauges[i]; ok {
					observer.ObserveInt64(g, spec.get(snapshot))
				}
			}

			return nil
		},
		inst.instruments...,
	)
	if regErr != nil {
		dm.logger.Error("dist meter: callback registration failed", slog.Any("err", regErr))

		return
	}

	dm.metricRegistration = reg
}

// applySet stores item locally and optionally replicates to other owners.
// replicate indicates whether replication fan-out should occur (false for replica writes).
func (dm *DistMemory) applySet(ctx context.Context, item *cache.Item, replicate bool) {
	sh := dm.shardFor(item.Key)
	dm.recordOriginalPrimary(sh, item.Key)
	sh.items.Set(item.Key, item)

	if !replicate || dm.ring == nil {
		return
	}

	owners := dm.ring.Lookup(item.Key)
	transport := dm.loadTransport()

	if len(owners) <= 1 || transport == nil {
		return
	}

	dm.metrics.replicaFanoutSet.Add(int64(len(owners) - 1))

	for _, oid := range owners[1:] { // skip primary
		if oid == dm.localNode.ID {
			continue
		}

		_ = dm.replicateSetWithSpan(ctx, transport, oid, item)
	}
}

// replicateSetWithSpan opens a child span around a single replica
// ForwardSet so operators can attribute fan-out latency per peer, and
// returns the underlying transport error for callers that count acks
// or queue hints. Used by both applySet's incoming-replica fan-out and
// the primary Set's replicateTo loop, so the trace tree shape is the
// same regardless of which path drove the replication.
func (dm *DistMemory) replicateSetWithSpan(ctx context.Context, transport DistTransport, oid cluster.NodeID, item *cache.Item) error {
	ctx, span := dm.tracer.Start(
		ctx, "dist.replicate.set",
		trace.WithAttributes(attribute.String("peer.id", string(oid))),
	)
	defer span.End()

	err := transport.ForwardSet(ctx, string(oid), item, false)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}

	return err
}

// recordOriginalPrimary stores first-seen primary owner for key.
func (dm *DistMemory) recordOriginalPrimary(sh *distShard, key string) {
	if sh == nil || sh.originalPrimary == nil || dm.ring == nil {
		return
	}

	sh.originalPrimaryMu.RLock()

	_, exists := sh.originalPrimary[key]
	sh.originalPrimaryMu.RUnlock()

	if exists {
		return
	}

	owners := dm.ring.Lookup(key)
	if len(owners) == 0 {
		return
	}

	sh.originalPrimaryMu.Lock()

	if _, exists2 := sh.originalPrimary[key]; !exists2 {
		sh.originalPrimary[key] = owners[0]
	}

	sh.originalPrimaryMu.Unlock()

	// Also record full owner set baseline if not yet present.
	if sh.originalOwners != nil {
		sh.originalOwnersMu.Lock()

		if _, ok := sh.originalOwners[key]; !ok {
			cp := make([]cluster.NodeID, len(owners))
			copy(cp, owners)

			sh.originalOwners[key] = cp
		}

		sh.originalOwnersMu.Unlock()
	}
}

// applyRemove deletes locally and optionally fan-outs removal to replicas.
func (dm *DistMemory) applyRemove(ctx context.Context, key string, replicate bool) {
	sh := dm.shardFor(key)
	// capture version from existing item (if any) and increment for tombstone
	var nextVer uint64

	if it, ok := sh.items.Get(key); ok && it != nil {
		ci := it // already *cache.Item (ConcurrentMap stores *cache.Item)

		nextVer = ci.Version + 1
	}

	if nextVer == 0 { // no prior item seen; allocate monotonic tomb version
		nextVer = dm.tombVersionCounter.Add(1)
	}

	sh.items.Remove(key)

	sh.tombs[key] = tombstone{version: nextVer, origin: string(dm.localNode.ID), at: time.Now()}
	dm.metrics.tombstonesActive.Store(dm.countTombstones())

	transport := dm.loadTransport()
	if !replicate || dm.ring == nil || transport == nil {
		return
	}

	owners := dm.ring.Lookup(key)
	if len(owners) <= 1 {
		return
	}

	dm.metrics.replicaFanoutRemove.Add(int64(len(owners) - 1))

	for _, oid := range owners[1:] {
		if oid == dm.localNode.ID {
			continue
		}

		dm.replicateRemoveWithSpan(ctx, transport, oid, key)
	}
}

// replicateRemoveWithSpan mirrors replicateSetWithSpan for tombstone
// fan-out — keeps the trace tree symmetric so a Set followed by a
// Remove has the same shape under tracing.
func (dm *DistMemory) replicateRemoveWithSpan(ctx context.Context, transport DistTransport, oid cluster.NodeID, key string) {
	ctx, span := dm.tracer.Start(
		ctx, "dist.replicate.remove",
		trace.WithAttributes(attribute.String("peer.id", string(oid))),
	)
	defer span.End()

	err := transport.ForwardRemove(ctx, string(oid), key, false)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
}

// runHeartbeatTick runs one heartbeat iteration (best-effort).
func (dm *DistMemory) runHeartbeatTick(ctx context.Context) { //nolint:revive
	if dm.loadTransport() == nil || dm.membership == nil {
		return
	}

	now := time.Now()

	peers := dm.membership.List()
	// optional sampling
	if dm.hbSampleSize > 0 && dm.hbSampleSize < len(peers) {
		// Fisher–Yates partial shuffle for first sampleCount elements
		sampleCount := dm.hbSampleSize
		for i := range sampleCount { // Go 1.22 int range form
			swapIndex := i
			span := len(peers) - i

			if span > 1 {
				idxBig, err := rand.Int(rand.Reader, big.NewInt(int64(span)))
				if err == nil {
					swapIndex = i + int(idxBig.Int64())
				}
			}

			peers[i], peers[swapIndex] = peers[swapIndex], peers[i]
		}

		peers = peers[:sampleCount]
	}

	for _, node := range peers { // rename for clarity
		if node.ID == dm.localNode.ID {
			continue
		}

		dm.evaluateLiveness(ctx, now, node)
	}
}

// evaluateLiveness applies timeout-based transitions then performs a probe.
func (dm *DistMemory) evaluateLiveness(ctx context.Context, now time.Time, node *cluster.Node) {
	elapsed := now.Sub(node.LastSeen)

	if dm.hbDeadAfter > 0 && elapsed > dm.hbDeadAfter { // prune dead
		if dm.membership.Remove(node.ID) {
			dm.metrics.nodesRemoved.Add(1)
			dm.metrics.nodesDead.Add(1)

			dm.logger.Warn(
				"peer pruned (dead)",
				slog.String("peer_id", string(node.ID)),
				slog.String("peer_addr", node.Address),
				slog.Duration("elapsed_since_seen", elapsed),
			)
		}

		return
	}

	if dm.hbSuspectAfter > 0 && elapsed > dm.hbSuspectAfter && node.State == cluster.NodeAlive { // suspect
		dm.membership.Mark(node.ID, cluster.NodeSuspect)
		dm.metrics.nodesSuspect.Add(1)

		dm.logger.Info(
			"peer marked suspect (timeout)",
			slog.String("peer_id", string(node.ID)),
			slog.Duration("elapsed_since_seen", elapsed),
		)
	}

	transport := dm.loadTransport()
	if transport == nil {
		return
	}

	ctxHealth, cancel := context.WithTimeout(ctx, dm.hbInterval/2)
	err := transport.Health(ctxHealth, string(node.ID))

	cancel()

	if err != nil {
		dm.metrics.heartbeatFailure.Add(1)
		dm.handleProbeFailure(ctx, transport, node, err)

		return
	}

	dm.metrics.heartbeatSuccess.Add(1)
	// Mark alive (refresh LastSeen, clear suspicion)
	dm.membership.Mark(node.ID, cluster.NodeAlive)
}

// handleProbeFailure runs the SWIM indirect-probe refutation path on
// a direct-probe failure: when indirect probes are enabled and any
// relay confirms the target is reachable, the direct failure is
// dismissed as caller-side. Otherwise the target is escalated to
// suspect. Extracted from evaluateLiveness to keep that function under
// the function-length lint cap.
func (dm *DistMemory) handleProbeFailure(ctx context.Context, transport DistTransport, node *cluster.Node, directErr error) {
	if dm.indirectProbeK > 0 && dm.indirectProbeReachable(ctx, transport, node.ID) {
		dm.metrics.indirectProbeRefuted.Add(1)

		dm.logger.Info(
			"peer probe refuted by indirect probe",
			slog.String("peer_id", string(node.ID)),
			slog.Any("direct_err", directErr),
		)

		// Refresh LastSeen — target is alive per the indirect path.
		dm.membership.Mark(node.ID, cluster.NodeAlive)

		return
	}

	if node.State != cluster.NodeAlive {
		return
	}

	dm.membership.Mark(node.ID, cluster.NodeSuspect)
	dm.metrics.nodesSuspect.Add(1)

	dm.logger.Info(
		"peer marked suspect (probe failed)",
		slog.String("peer_id", string(node.ID)),
		slog.Any("err", directErr),
	)
}

// indirectProbeReachable runs up to indirectProbeK indirect probes
// against `targetID` via random alive peers. Returns true the moment
// any relay reports the target reachable — the caller's direct probe
// is then refuted and the target is treated as alive. Returns false if
// no relay can confirm reachability (genuinely down, or no relays
// available).
//
// Probes run sequentially with a per-probe timeout to bound the
// caller's heartbeat tick latency. Sequential is correct here: the
// first success short-circuits, and a parallel implementation would
// pay the full timeout on a fully-down target.
func (dm *DistMemory) indirectProbeReachable(ctx context.Context, transport DistTransport, targetID cluster.NodeID) bool {
	relays := dm.pickIndirectRelays(targetID, dm.indirectProbeK)
	if len(relays) == 0 {
		return false
	}

	timeout := dm.indirectProbeTimeout
	if timeout <= 0 {
		timeout = dm.hbInterval / 2
	}

	for _, relay := range relays {
		probeCtx, cancel := context.WithTimeout(ctx, timeout)
		err := transport.IndirectHealth(probeCtx, string(relay.ID), string(targetID))

		cancel()

		if err == nil {
			dm.metrics.indirectProbeSuccess.Add(1)

			return true
		}

		dm.metrics.indirectProbeFailure.Add(1)
	}

	return false
}

// pickIndirectRelays returns up to k random alive members other than
// self and target. When fewer than k qualify, returns whatever is
// available (no padding). Uses crypto/rand for selection to keep the
// pre-existing G404-free posture in this file.
func (dm *DistMemory) pickIndirectRelays(targetID cluster.NodeID, k int) []*cluster.Node {
	if dm.membership == nil || k <= 0 {
		return nil
	}

	const relayPrealloc = 8

	candidates := make([]*cluster.Node, 0, relayPrealloc)

	for _, n := range dm.membership.List() {
		if n == nil {
			continue
		}

		if n.ID == dm.localNode.ID || n.ID == targetID {
			continue
		}

		if n.State != cluster.NodeAlive {
			continue
		}

		candidates = append(candidates, n)
	}

	if len(candidates) <= k {
		return candidates
	}

	// Fisher–Yates partial shuffle for the first k positions, using
	// crypto/rand to match the rest of this file's randomness posture.
	for i := range k {
		span := len(candidates) - i

		idxBig, err := rand.Int(rand.Reader, big.NewInt(int64(span)))
		if err != nil {
			continue
		}

		swap := i + int(idxBig.Int64())

		candidates[i], candidates[swap] = candidates[swap], candidates[i]
	}

	return candidates[:k]
}
