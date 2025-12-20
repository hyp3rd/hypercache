package backend

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"hash"
	"hash/fnv"
	"math/big"
	mrand "math/rand"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hyp3rd/hypercache/internal/cluster"
	"github.com/hyp3rd/hypercache/internal/sentinel"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

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

var errNoTransport = errors.New("no transport")

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
	transport  DistTransport
	httpServer *distHTTPServer // optional internal HTTP server
	metrics    distMetrics
	// configuration (static for now, future: dynamic membership/gossip)
	replication  int
	virtualNodes int
	nodeAddr     string
	nodeID       string
	seeds        []string // static seed node addresses

	// heartbeat / failure detection (experimental)
	hbInterval     time.Duration
	hbSuspectAfter time.Duration
	hbDeadAfter    time.Duration
	stopCh         chan struct{}

	// heartbeat sampling (Phase 2)
	hbSampleSize int // number of random peers to probe each tick (0=probe all)

	// consistency / versioning (initial)
	readConsistency  ConsistencyLevel
	writeConsistency ConsistencyLevel
	versionCounter   uint64 // global monotonic for this node (lamport-like)

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

	// replica-only diff scan limits
	replicaDiffMaxPerTick int // 0 = unlimited

	// shedding / cleanup of keys we no longer own (grace period before delete to aid late reads / hinted handoff)
	removalGracePeriod time.Duration // if >0 we delay deleting keys we no longer own; after grace we remove locally
}

const (
	defaultRebalanceBatchSize     = 128
	defaultRebalanceMaxConcurrent = 2
)

var errUnexpectedBackendType = errors.New("backend: unexpected backend type") // stable error (no dynamic wrapping needed)

// hintedEntry represents a deferred replica write.
type hintedEntry struct {
	item   *cache.Item
	expire time.Time
	size   int64 // approximate bytes for global cap accounting
}

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
func WithDistReplicaDiffMaxPerTick(n int) DistMemoryOption { //nolint:ireturn
	return func(dm *DistMemory) {
		if n > 0 {
			dm.replicaDiffMaxPerTick = n
		}
	}
}

// WithDistRemovalGrace sets grace period before shedding data for keys we no longer own (<=0 immediate remove disabled for now).
func WithDistRemovalGrace(d time.Duration) DistMemoryOption { //nolint:ireturn
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
func (dm *DistMemory) BuildMerkleTree() *MerkleTree { //nolint:ireturn
	chunkSize := dm.merkleChunkSize
	if chunkSize <= 0 {
		chunkSize = defaultMerkleChunkSize
	}

	entries := dm.merkleEntries()
	if len(entries) == 0 {
		return &MerkleTree{ChunkSize: chunkSize}
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].k < entries[j].k })

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
func (mt *MerkleTree) DiffLeafRanges(other *MerkleTree) []int { //nolint:ireturn
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
func (dm *DistMemory) SyncWith(ctx context.Context, nodeID string) error { //nolint:ireturn
	if dm.transport == nil {
		return errNoTransport
	}

	startFetch := time.Now()

	remoteTree, err := dm.transport.FetchMerkle(ctx, nodeID)
	if err != nil {
		return err
	}

	fetchDur := time.Since(startFetch)
	atomic.StoreInt64(&dm.metrics.merkleFetchNanos, fetchDur.Nanoseconds())

	startBuild := time.Now()
	localTree := dm.BuildMerkleTree()
	buildDur := time.Since(startBuild)
	atomic.StoreInt64(&dm.metrics.merkleBuildNanos, buildDur.Nanoseconds())

	entries := dm.sortedMerkleEntries()
	startDiff := time.Now()
	diffs := localTree.DiffLeafRanges(remoteTree)
	diffDur := time.Since(startDiff)
	atomic.StoreInt64(&dm.metrics.merkleDiffNanos, diffDur.Nanoseconds())

	missing := dm.resolveMissingKeys(ctx, nodeID, entries)

	dm.applyMerkleDiffs(ctx, nodeID, entries, diffs, localTree.ChunkSize)

	for k := range missing { // missing = remote-only keys
		dm.fetchAndAdopt(ctx, nodeID, k)
	}

	if len(diffs) == 0 && len(missing) == 0 {
		return nil
	}

	atomic.AddInt64(&dm.metrics.merkleSyncs, 1)

	return nil
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
	return func(dm *DistMemory) { dm.transport = t }
}

// WithDistHeartbeatSample sets how many random peers to probe per heartbeat tick (0=all).
func WithDistHeartbeatSample(k int) DistMemoryOption { //nolint:ireturn
	return func(dm *DistMemory) { dm.hbSampleSize = k }
}

// SetTransport sets the transport post-construction (testing helper).
func (dm *DistMemory) SetTransport(t DistTransport) { dm.transport = t }

// WithDistHeartbeat configures heartbeat interval and suspect/dead thresholds.
// If interval <= 0 heartbeat is disabled.
func WithDistHeartbeat(interval, suspectAfter, deadAfter time.Duration) DistMemoryOption {
	return func(dm *DistMemory) {
		dm.hbInterval = interval
		dm.hbSuspectAfter = suspectAfter
		dm.hbDeadAfter = deadAfter
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
func WithDistHintMaxTotal(n int) DistMemoryOption { //nolint:ireturn
	return func(dm *DistMemory) {
		if n > 0 {
			dm.hintMaxTotal = n
		}
	}
}

// WithDistHintMaxBytes sets an approximate byte cap for all queued hints.
func WithDistHintMaxBytes(b int64) DistMemoryOption { //nolint:ireturn
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

// NewDistMemory creates a new DistMemory backend.
func NewDistMemory(ctx context.Context, opts ...DistMemoryOption) (IBackend[DistMemory], error) {
	dm := &DistMemory{
		shardCount:       defaultDistShardCount,
		replication:      1,
		readConsistency:  ConsistencyOne,
		writeConsistency: ConsistencyQuorum,
		latency:          newDistLatencyCollector(),
	}
	for _, opt := range opts {
		opt(dm)
	}

	dm.ensureShardConfig()
	dm.initMembershipIfNeeded()
	dm.tryStartHTTP(ctx)
	dm.startHeartbeatIfEnabled(ctx)
	dm.startHintReplayIfEnabled(ctx)
	dm.startGossipIfEnabled()
	dm.startAutoSyncIfEnabled(ctx)
	dm.startTombstoneSweeper()
	dm.startRebalancerIfEnabled(ctx)

	return dm, nil
}

// NewDistMemoryWithConfig builds a DistMemory from an external dist.Config shape without introducing a direct import here.
// Accepts a generic 'cfg' to avoid adding a dependency layer; expects exported fields matching internal/dist Config.
func NewDistMemoryWithConfig(ctx context.Context, cfg any, opts ...DistMemoryOption) (IBackend[DistMemory], error) { //nolint:ireturn
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
) []DistMemoryOption { //nolint:ireturn
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
		// iterate channel to count; ConcurrentMap has Count helper
		// but we call Count for each shard
		for range s.items.IterBuffered() {
			// ineff: but simple; optimize later with sized field
			total++
		}
	}

	return total
}

// Get fetches item.
func (dm *DistMemory) Get(ctx context.Context, key string) (*cache.Item, bool) { //nolint:ireturn
	start := time.Now()
	defer func() {
		if dm.latency != nil {
			dm.latency.observe(opGet, time.Since(start))
		}
	}()

	if dm.readConsistency == ConsistencyOne { // fast local path
		if it, ok := dm.shardFor(key).items.Get(key); ok {
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

// Set stores item.
func (dm *DistMemory) Set(ctx context.Context, item *cache.Item) error { //nolint:ireturn
	err := item.Valid()
	if err != nil {
		return err
	}

	start := time.Now()
	defer func() {
		if dm.latency != nil {
			dm.latency.observe(opSet, time.Since(start))
		}
	}()

	atomic.AddInt64(&dm.metrics.writeAttempts, 1)

	owners := dm.lookupOwners(item.Key)
	if len(owners) == 0 {
		return sentinel.ErrNotOwner
	}

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
	item.Version = atomic.AddUint64(&dm.versionCounter, 1)
	item.Origin = string(dm.localNode.ID)
	item.LastUpdated = time.Now()
	dm.applySet(ctx, item, false)

	acks := 1 + dm.replicateTo(ctx, item, owners[1:])
	atomic.AddInt64(&dm.metrics.writeAcks, int64(acks))

	needed := dm.requiredAcks(len(owners), dm.writeConsistency)
	if acks < needed {
		atomic.AddInt64(&dm.metrics.writeQuorumFailures, 1)

		return sentinel.ErrQuorumFailed
	}

	return nil
}

// --- Consistency helper methods. ---

// List aggregates items (no ordering, then filters applied per interface contract not yet integrated; kept simple).
func (dm *DistMemory) List(_ context.Context, _ ...IFilter) ([]*cache.Item, error) {
	items := make([]*cache.Item, 0, listPrealloc)
	for _, s := range dm.shards {
		for kv := range s.items.IterBuffered() {
			cloned := kv.Val

			items = append(items, &cloned)
		}
	}

	return items, nil
}

// Remove deletes keys.
func (dm *DistMemory) Remove(ctx context.Context, keys ...string) error { //nolint:ireturn
	start := time.Now()
	defer func() {
		if dm.latency != nil {
			dm.latency.observe(opRemove, time.Since(start))
		}
	}()

	for _, key := range keys {
		if dm.ownsKeyInternal(key) { // primary path
			dm.applyRemove(ctx, key, true)

			continue
		}

		if dm.transport == nil { // non-owner without transport
			return sentinel.ErrNotOwner
		}

		owners := dm.ring.Lookup(key)
		if len(owners) == 0 {
			continue
		}

		atomic.AddInt64(&dm.metrics.forwardRemove, 1)

		_ = dm.transport.ForwardRemove(ctx, string(owners[0]), key, true) //nolint:errcheck // best-effort
	}

	return nil
}

// Clear wipes all shards.
func (dm *DistMemory) Clear(ctx context.Context) error { //nolint:ireturn
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

// DebugDropLocal removes a key only from the local shard (for tests / read-repair validation).
func (dm *DistMemory) DebugDropLocal(key string) { dm.shardFor(key).items.Remove(key) }

// DebugInject stores an item directly into the local shard (no replication / ownership checks) for tests.
func (dm *DistMemory) DebugInject(it *cache.Item) { //nolint:ireturn
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
func (dm *DistMemory) LocalNodeAddr() string { //nolint:ireturn
	return dm.nodeAddr
}

// SetLocalNode manually sets the local node (testing helper before starting HTTP).
func (dm *DistMemory) SetLocalNode(node *cluster.Node) { //nolint:ireturn
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
	forwardGet                   int64
	forwardSet                   int64
	forwardRemove                int64
	replicaFanoutSet             int64
	replicaFanoutRemove          int64
	readRepair                   int64
	replicaGetMiss               int64
	heartbeatSuccess             int64
	heartbeatFailure             int64
	nodesSuspect                 int64 // number of times a node transitioned to suspect
	nodesDead                    int64 // number of times a node transitioned to dead/pruned
	nodesRemoved                 int64
	versionConflicts             int64 // times a newer version (or tie-broken origin) replaced previous candidate
	versionTieBreaks             int64 // subset of conflicts decided by origin tie-break
	readPrimaryPromote           int64 // times read path skipped unreachable primary and promoted next owner
	hintedQueued                 int64 // hints queued
	hintedReplayed               int64 // hints successfully replayed
	hintedExpired                int64 // hints expired before delivery
	hintedDropped                int64 // hints dropped due to non-not-found transport errors
	hintedGlobalDropped          int64 // hints dropped due to global caps (count/bytes)
	hintedBytes                  int64 // approximate total bytes currently queued (best-effort)
	merkleSyncs                  int64 // merkle sync operations completed
	merkleKeysPulled             int64 // keys applied during sync
	merkleBuildNanos             int64 // last build duration (ns)
	merkleDiffNanos              int64 // last diff duration (ns)
	merkleFetchNanos             int64 // last remote fetch duration (ns)
	autoSyncLoops                int64 // number of auto-sync ticks executed
	tombstonesActive             int64 // approximate active tombstones
	tombstonesPurged             int64 // cumulative purged tombstones
	writeQuorumFailures          int64 // number of write operations that failed quorum
	writeAcks                    int64 // cumulative replica write acks (includes primary)
	writeAttempts                int64 // total write operations attempted (Set)
	rebalancedKeys               int64 // keys migrated during rebalancing
	rebalanceBatches             int64 // number of batches processed
	rebalanceThrottle            int64 // times rebalance was throttled due to concurrency limits
	rebalanceLastNanos           int64 // duration of last full rebalance scan (ns)
	rebalanceReplicaDiff         int64 // number of keys whose value was pushed to newly added replicas (replica-only diff)
	rebalanceReplicaDiffThrottle int64 // number of times replica diff scan exited early due to per-tick limit
	rebalancedPrimary            int64 // number of keys whose primary ownership changed (migrations to new primary)
}

// DistMetrics snapshot.
type DistMetrics struct {
	ForwardGet                   int64
	ForwardSet                   int64
	ForwardRemove                int64
	ReplicaFanoutSet             int64
	ReplicaFanoutRemove          int64
	ReadRepair                   int64
	ReplicaGetMiss               int64
	HeartbeatSuccess             int64
	HeartbeatFailure             int64
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
	MerkleSyncs                  int64
	MerkleKeysPulled             int64
	MerkleBuildNanos             int64
	MerkleDiffNanos              int64
	MerkleFetchNanos             int64
	AutoSyncLoops                int64
	LastAutoSyncNanos            int64
	LastAutoSyncError            string
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
func (dm *DistMemory) Metrics() DistMetrics {
	lastErr := ""
	if v := dm.lastAutoSyncError.Load(); v != nil {
		if s, ok := v.(string); ok {
			lastErr = s
		}
	}

	var mv uint64

	var alive, suspect, dead int64

	if dm.membership != nil {
		mv = dm.membership.Version()
		for _, n := range dm.membership.List() {
			switch n.State.String() {
			case "alive":
				alive++
			case "suspect":
				suspect++
			case "dead":
				dead++
			default: // ignore future states
			}
		}
	}

	return DistMetrics{
		ForwardGet:                   atomic.LoadInt64(&dm.metrics.forwardGet),
		ForwardSet:                   atomic.LoadInt64(&dm.metrics.forwardSet),
		ForwardRemove:                atomic.LoadInt64(&dm.metrics.forwardRemove),
		ReplicaFanoutSet:             atomic.LoadInt64(&dm.metrics.replicaFanoutSet),
		ReplicaFanoutRemove:          atomic.LoadInt64(&dm.metrics.replicaFanoutRemove),
		ReadRepair:                   atomic.LoadInt64(&dm.metrics.readRepair),
		ReplicaGetMiss:               atomic.LoadInt64(&dm.metrics.replicaGetMiss),
		HeartbeatSuccess:             atomic.LoadInt64(&dm.metrics.heartbeatSuccess),
		HeartbeatFailure:             atomic.LoadInt64(&dm.metrics.heartbeatFailure),
		NodesSuspect:                 atomic.LoadInt64(&dm.metrics.nodesSuspect),
		NodesDead:                    atomic.LoadInt64(&dm.metrics.nodesDead),
		NodesRemoved:                 atomic.LoadInt64(&dm.metrics.nodesRemoved),
		VersionConflicts:             atomic.LoadInt64(&dm.metrics.versionConflicts),
		VersionTieBreaks:             atomic.LoadInt64(&dm.metrics.versionTieBreaks),
		ReadPrimaryPromote:           atomic.LoadInt64(&dm.metrics.readPrimaryPromote),
		HintedQueued:                 atomic.LoadInt64(&dm.metrics.hintedQueued),
		HintedReplayed:               atomic.LoadInt64(&dm.metrics.hintedReplayed),
		HintedExpired:                atomic.LoadInt64(&dm.metrics.hintedExpired),
		HintedDropped:                atomic.LoadInt64(&dm.metrics.hintedDropped),
		HintedGlobalDropped:          atomic.LoadInt64(&dm.metrics.hintedGlobalDropped),
		HintedBytes:                  atomic.LoadInt64(&dm.metrics.hintedBytes),
		MerkleSyncs:                  atomic.LoadInt64(&dm.metrics.merkleSyncs),
		MerkleKeysPulled:             atomic.LoadInt64(&dm.metrics.merkleKeysPulled),
		MerkleBuildNanos:             atomic.LoadInt64(&dm.metrics.merkleBuildNanos),
		MerkleDiffNanos:              atomic.LoadInt64(&dm.metrics.merkleDiffNanos),
		MerkleFetchNanos:             atomic.LoadInt64(&dm.metrics.merkleFetchNanos),
		AutoSyncLoops:                atomic.LoadInt64(&dm.metrics.autoSyncLoops),
		LastAutoSyncNanos:            dm.lastAutoSyncDuration.Load(),
		LastAutoSyncError:            lastErr,
		TombstonesActive:             atomic.LoadInt64(&dm.metrics.tombstonesActive),
		TombstonesPurged:             atomic.LoadInt64(&dm.metrics.tombstonesPurged),
		WriteQuorumFailures:          atomic.LoadInt64(&dm.metrics.writeQuorumFailures),
		WriteAcks:                    atomic.LoadInt64(&dm.metrics.writeAcks),
		WriteAttempts:                atomic.LoadInt64(&dm.metrics.writeAttempts),
		RebalancedKeys:               atomic.LoadInt64(&dm.metrics.rebalancedKeys),
		RebalanceBatches:             atomic.LoadInt64(&dm.metrics.rebalanceBatches),
		RebalanceThrottle:            atomic.LoadInt64(&dm.metrics.rebalanceThrottle),
		RebalanceLastNanos:           atomic.LoadInt64(&dm.metrics.rebalanceLastNanos),
		RebalancedReplicaDiff:        atomic.LoadInt64(&dm.metrics.rebalanceReplicaDiff),
		RebalanceReplicaDiffThrottle: atomic.LoadInt64(&dm.metrics.rebalanceReplicaDiffThrottle),
		RebalancedPrimary:            atomic.LoadInt64(&dm.metrics.rebalancedPrimary),
		MembershipVersion:            mv,
		MembersAlive:                 alive,
		MembersSuspect:               suspect,
		MembersDead:                  dead,
	}
}

// DistMembershipSnapshot returns lightweight membership view (states & version).
func (dm *DistMemory) DistMembershipSnapshot() map[string]any { //nolint:ireturn
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
func (dm *DistMemory) LatencyHistograms() map[string][]uint64 { //nolint:ireturn
	if dm.latency == nil {
		return nil
	}

	return dm.latency.snapshot()
}

// Stop stops heartbeat loop if running.
func (dm *DistMemory) Stop(ctx context.Context) error { //nolint:ireturn
	if dm.stopCh != nil {
		close(dm.stopCh)

		dm.stopCh = nil
	}

	if dm.hintStopCh != nil {
		close(dm.hintStopCh)

		dm.hintStopCh = nil
	}

	if dm.gossipStopCh != nil {
		close(dm.gossipStopCh)

		dm.gossipStopCh = nil
	}

	if dm.autoSyncStopCh != nil {
		close(dm.autoSyncStopCh)

		dm.autoSyncStopCh = nil
	}

	if dm.httpServer != nil {
		err := dm.httpServer.stop(ctx) // best-effort
		if err != nil {
			return err
		}
	}

	if dm.tombStopCh != nil { // stop tomb sweeper
		close(dm.tombStopCh)
	}

	return nil
}

// --- Sync helper methods (placed after exported methods to satisfy ordering linter) ---

// IsOwner reports whether this node is an owner (primary or replica) for key.
// Exported for tests / external observability (thin wrapper over internal logic).
func (dm *DistMemory) IsOwner(key string) bool { //nolint:ireturn
	return dm.ownsKeyInternal(key)
}

// AddPeer adds a peer address into local membership (best-effort, no network validation).
// If the peer already exists (by address) it's ignored. Used by tests to simulate join propagation.
func (dm *DistMemory) AddPeer(address string) { //nolint:ireturn
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
}

// RemovePeer removes a peer by address (best-effort) to simulate node leave in tests.
func (dm *DistMemory) RemovePeer(address string) { //nolint:ireturn
	if dm == nil || dm.membership == nil || address == "" {
		return
	}

	for _, n := range dm.membership.List() {
		if n.Address == address {
			dm.membership.Remove(n.ID)

			return
		}
	}
}

// sortedMerkleEntries returns merkle entries sorted by key.
func (dm *DistMemory) sortedMerkleEntries() []merkleKV { //nolint:ireturn
	entries := dm.merkleEntries()
	sort.Slice(entries, func(i, j int) bool { return entries[i].k < entries[j].k })

	return entries
}

// resolveMissingKeys enumerates remote-only keys using in-process or HTTP listing.
func (dm *DistMemory) resolveMissingKeys(ctx context.Context, nodeID string, entries []merkleKV) map[string]struct{} { //nolint:ireturn
	missing := dm.enumerateRemoteOnlyKeys(nodeID, entries)
	if len(missing) != 0 {
		return missing
	}

	httpT, ok := dm.transport.(*DistHTTPTransport)
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
		atomic.AddInt64(&dm.metrics.merkleKeysPulled, int64(len(mset)))
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
) { //nolint:ireturn
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

// enumerateRemoteOnlyKeys returns keys present only on the remote side (best-effort, in-process only).
func (dm *DistMemory) enumerateRemoteOnlyKeys(nodeID string, local []merkleKV) map[string]struct{} { //nolint:ireturn
	missing := make(map[string]struct{})

	ip, ok := dm.transport.(*InProcessTransport)
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

		rch := shard.items.IterBuffered()
		for t := range rch {
			missing[t.Key] = struct{}{}
		}
	}

	for _, e := range local { // remove any that we already have
		delete(missing, e.k)
	}

	return missing
}

// fetchAndAdopt pulls a key from a remote node and adopts it if it's newer or absent locally.
func (dm *DistMemory) fetchAndAdopt(ctx context.Context, nodeID, key string) {
	it, ok, gerr := dm.transport.ForwardGet(ctx, nodeID, key)
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
			atomic.StoreInt64(&dm.metrics.tombstonesActive, dm.countTombstones())
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
		atomic.StoreInt64(&dm.metrics.tombstonesActive, dm.countTombstones())
	}

	if cur, okLocal := sh.items.Get(key); !okLocal || it.Version > cur.Version {
		dm.applySet(ctx, it, false)
		atomic.AddInt64(&dm.metrics.merkleKeysPulled, 1)
	}
}

// merkleEntries gathers key/version pairs from all shards.
func (dm *DistMemory) merkleEntries() []merkleKV {
	entries := make([]merkleKV, 0, merklePreallocEntries)

	for _, shard := range dm.shards {
		if shard == nil {
			continue
		}

		ch := shard.items.IterBuffered()
		for t := range ch {
			entries = append(entries, merkleKV{k: t.Key, v: t.Val.Version})
		}

		for k, ts := range shard.tombs { // include tombstones
			entries = append(entries, merkleKV{k: k, v: ts.version})
		}
	}

	return entries
}

// startTombstoneSweeper launches periodic compaction if configured.
func (dm *DistMemory) startTombstoneSweeper() { //nolint:ireturn
	if dm.tombstoneTTL <= 0 || dm.tombstoneSweepInt <= 0 {
		return
	}

	dm.tombStopCh = make(chan struct{})
	go func() {
		ticker := time.NewTicker(dm.tombstoneSweepInt)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				purged := dm.compactTombstones()
				if purged > 0 {
					atomic.AddInt64(&dm.metrics.tombstonesPurged, purged)
				}

				atomic.StoreInt64(&dm.metrics.tombstonesActive, dm.countTombstones())

			case <-dm.tombStopCh:
				return
			}
		}
	}()
}

// compactTombstones removes expired tombstones based on TTL, returns count purged.
func (dm *DistMemory) compactTombstones() int64 { //nolint:ireturn
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
func (dm *DistMemory) countTombstones() int64 { //nolint:ireturn
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
func (dm *DistMemory) startRebalancerIfEnabled(ctx context.Context) { //nolint:ireturn
	if dm.rebalanceInterval <= 0 || dm.membership == nil || dm.ring == nil {
		return
	}

	if dm.rebalanceBatchSize <= 0 {
		dm.rebalanceBatchSize = defaultRebalanceBatchSize
	}

	if dm.rebalanceMaxConcurrent <= 0 {
		dm.rebalanceMaxConcurrent = defaultRebalanceMaxConcurrent
	}

	dm.rebalanceStopCh = make(chan struct{})

	go dm.rebalanceLoop(ctx)
}

func (dm *DistMemory) rebalanceLoop(ctx context.Context) { //nolint:ireturn
	ticker := time.NewTicker(dm.rebalanceInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			dm.runRebalanceTick(ctx)
		case <-dm.rebalanceStopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// runRebalanceTick performs a lightweight ownership diff and migrates keys best-effort.
func (dm *DistMemory) runRebalanceTick(ctx context.Context) { //nolint:ireturn
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

	atomic.StoreInt64(&dm.metrics.rebalanceLastNanos, time.Since(start).Nanoseconds())
	dm.lastRebalanceVersion.Store(mv)
}

// collectRebalanceCandidates scans shards for items whose primary ownership changed.
// Note: we copy items (not pointers) to avoid pointer-to-loop-variable issues.
func (dm *DistMemory) collectRebalanceCandidates() []cache.Item { //nolint:ireturn
	if len(dm.shards) == 0 {
		return nil
	}

	const capHint = 1024

	out := make([]cache.Item, 0, capHint)
	for _, sh := range dm.shards {
		if sh == nil {
			continue
		}

		for kv := range sh.items.IterBuffered() { // snapshot iteration
			it := kv.Val
			if dm.shouldRebalance(sh, &it) {
				out = append(out, it)
			}
		}
	}

	return out
}

// shouldRebalance determines if the item should be migrated away.
// Triggers when this node lost all ownership or was previously primary and is no longer.
func (dm *DistMemory) shouldRebalance(sh *distShard, it *cache.Item) bool { //nolint:ireturn
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
func (dm *DistMemory) replicateNewReplicas(ctx context.Context) { //nolint:ireturn
	if dm.ring == nil || dm.transport == nil {
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

func (dm *DistMemory) replDiffShard(ctx context.Context, sh *distShard, processed, limit int) int { //nolint:ireturn
	for kv := range sh.items.IterBuffered() {
		if limit > 0 && processed >= limit {
			return processed
		}

		it := kv.Val

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

		processed = dm.sendReplicaDiff(ctx, &it, newRepls, processed, limit)
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

func (*DistMemory) computeNewReplicas(sh *distShard, key string, owners []cluster.NodeID) []cluster.NodeID { //nolint:ireturn
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
) int { //nolint:ireturn
	for _, rid := range repls {
		if rid == dm.localNode.ID {
			continue
		}

		_ = dm.transport.ForwardSet(ctx, string(rid), it, false) //nolint:errcheck
		atomic.AddInt64(&dm.metrics.replicaFanoutSet, 1)
		atomic.AddInt64(&dm.metrics.rebalancedKeys, 1)
		atomic.AddInt64(&dm.metrics.rebalanceReplicaDiff, 1)

		processed++
		if limit > 0 && processed >= limit {
			atomic.AddInt64(&dm.metrics.rebalanceReplicaDiffThrottle, 1)

			return processed
		}
	}

	return processed
}

func (*DistMemory) setOwnerBaseline(sh *distShard, key string, owners []cluster.NodeID) { //nolint:ireturn
	sh.originalOwnersMu.Lock()

	cp := make([]cluster.NodeID, len(owners))
	copy(cp, owners)

	sh.originalOwners[key] = cp
	sh.originalOwnersMu.Unlock()
}

func (dm *DistMemory) maybeRecordRemoval(sh *distShard, key string) { //nolint:ireturn
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
//
//nolint:ireturn,revive
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
			atomic.AddInt64(&dm.metrics.rebalanceThrottle, 1)

			sem <- struct{}{}
		}

		batchItems := batch
		wg.Go(func() {
			defer func() { <-sem }()

			atomic.AddInt64(&dm.metrics.rebalanceBatches, 1)

			for i := range batchItems {
				itm := batchItems[i] // value copy
				dm.migrateIfNeeded(ctx, &itm)
			}
		})

		go func(batchItems []cache.Item) {
			defer wg.Done()
			defer func() { <-sem }()

			atomic.AddInt64(&dm.metrics.rebalanceBatches, 1)

			for i := range batchItems {
				itm := batchItems[i] // value copy
				dm.migrateIfNeeded(ctx, &itm)
			}
		}(batch)
	}

	wg.Wait()
}

// migrateIfNeeded forwards item to new primary if this node no longer owns it.
func (dm *DistMemory) migrateIfNeeded(ctx context.Context, item *cache.Item) { //nolint:ireturn
	owners := dm.lookupOwners(item.Key)
	if len(owners) == 0 || owners[0] == dm.localNode.ID {
		return
	}

	if dm.transport == nil {
		return
	}

	// increment metrics once per attempt (ownership changed). Success is best-effort.
	atomic.AddInt64(&dm.metrics.rebalancedKeys, 1)
	atomic.AddInt64(&dm.metrics.rebalancedPrimary, 1)

	_ = dm.transport.ForwardSet(ctx, string(owners[0]), item, true) //nolint:errcheck // best-effort

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
func (dm *DistMemory) shedRemovedKeys() { //nolint:ireturn
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

func (dm *DistMemory) shedShard(sh *distShard, now time.Time) { //nolint:ireturn
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
func foldMerkle(leaves [][]byte, hasher hash.Hash) []byte { //nolint:ireturn
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
func (dm *DistMemory) ensureShardConfig() { //nolint:ireturn
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
func (dm *DistMemory) initMembershipIfNeeded() { //nolint:ireturn
	if dm.membership == nil {
		dm.initStandaloneMembership()

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
}

// tryStartHTTP starts internal HTTP transport if not provided.
func (dm *DistMemory) tryStartHTTP(ctx context.Context) { //nolint:ireturn
	if dm.transport != nil || dm.nodeAddr == "" {
		return
	}

	server := newDistHTTPServer(dm.nodeAddr)

	err := server.start(ctx, dm)
	if err != nil { // best-effort
		return
	}

	dm.httpServer = server

	resolver := func(nodeID string) (string, bool) {
		if dm.membership != nil {
			for _, n := range dm.membership.List() {
				if string(n.ID) == nodeID {
					return "http://" + n.Address, true
				}
			}
		}

		if dm.localNode != nil && string(dm.localNode.ID) == nodeID {
			return "http://" + dm.localNode.Address, true
		}

		return "", false
	}

	dm.transport = NewDistHTTPTransport(2*time.Second, resolver)
}

// startHeartbeatIfEnabled launches heartbeat loop if configured.
func (dm *DistMemory) startHeartbeatIfEnabled(ctx context.Context) { //nolint:ireturn
	if dm.hbInterval > 0 && dm.transport != nil {
		dm.stopCh = make(chan struct{})
		go dm.heartbeatLoop(ctx)
	}
}

// lookupOwners returns ring owners slice for a key (nil if no ring).
func (dm *DistMemory) lookupOwners(key string) []cluster.NodeID { //nolint:ireturn
	if dm.ring == nil {
		return nil
	}

	return dm.ring.Lookup(key)
}

// requiredAcks computes required acknowledgements for given consistency level.
func (*DistMemory) requiredAcks(total int, lvl ConsistencyLevel) int { //nolint:ireturn
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
func (dm *DistMemory) getOne(ctx context.Context, key string, owners []cluster.NodeID) (*cache.Item, bool) { //nolint:ireturn
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
func (dm *DistMemory) tryLocalGet(key string, idx int, oid cluster.NodeID) (*cache.Item, bool) { //nolint:ireturn
	if oid != dm.localNode.ID { // not local owner
		return nil, false
	}

	if it, ok := dm.shardFor(key).items.Get(key); ok {
		if idx > 0 { // promotion
			atomic.AddInt64(&dm.metrics.readPrimaryPromote, 1)
		}

		return it, true
	}

	return nil, false
}

// tryRemoteGet attempts remote fetch for given owner; includes promotion + repair.
func (dm *DistMemory) tryRemoteGet(ctx context.Context, key string, idx int, oid cluster.NodeID) (*cache.Item, bool) { //nolint:ireturn
	if oid == dm.localNode.ID || dm.transport == nil { // skip local path or missing transport
		return nil, false
	}

	atomic.AddInt64(&dm.metrics.forwardGet, 1)

	it, ok, err := dm.transport.ForwardGet(ctx, string(oid), key)
	if errors.Is(err, sentinel.ErrBackendNotFound) { // owner unreachable -> promotion scenario
		if idx == 0 { // primary missing
			atomic.AddInt64(&dm.metrics.readPrimaryPromote, 1)
		}

		return nil, false
	}

	if !ok { // miss
		return nil, false
	}

	if idx > 0 { // promotion occurred
		atomic.AddInt64(&dm.metrics.readPrimaryPromote, 1)
	}

	// read repair: if we're an owner but local missing, replicate
	if dm.ownsKeyInternal(key) {
		if _, ok2 := dm.shardFor(key).items.Get(key); !ok2 {
			cloned := *it
			dm.applySet(ctx, &cloned, false)
			atomic.AddInt64(&dm.metrics.readRepair, 1)
		}
	}

	return it, true
}

// getWithConsistency performs quorum/all reads.
func (dm *DistMemory) getWithConsistency(ctx context.Context, key string, owners []cluster.NodeID) (*cache.Item, bool) { //nolint:ireturn
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
) []cluster.NodeID { //nolint:ireturn
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
) { //nolint:ireturn
	if dm.transport == nil || chosen == nil {
		return
	}

	for _, oid := range staleOwners {
		if oid == dm.localNode.ID { // local handled in repairReplicas
			continue
		}

		it, ok, err := dm.transport.ForwardGet(ctx, string(oid), key)
		if err != nil { // skip unreachable
			continue
		}

		if !ok || it.Version < chosen.Version || (it.Version == chosen.Version && it.Origin > chosen.Origin) {
			_ = dm.transport.ForwardSet(ctx, string(oid), chosen, false) //nolint:errcheck
			atomic.AddInt64(&dm.metrics.readRepair, 1)
		}
	}
}

// fetchOwner attempts to fetch item from given owner (local or remote) updating metrics.
func (dm *DistMemory) fetchOwner(ctx context.Context, key string, idx int, oid cluster.NodeID) (*cache.Item, bool) { //nolint:ireturn
	if oid == dm.localNode.ID { // local
		if it, ok := dm.shardFor(key).items.Get(key); ok {
			return it, true
		}

		return nil, false
	}

	it, ok, err := dm.transport.ForwardGet(ctx, string(oid), key)
	if errors.Is(err, sentinel.ErrBackendNotFound) { // promotion
		if idx == 0 {
			atomic.AddInt64(&dm.metrics.readPrimaryPromote, 1)
		}

		return nil, false
	}

	if !ok {
		return nil, false
	}

	if idx > 0 { // earlier owner skipped
		atomic.AddInt64(&dm.metrics.readPrimaryPromote, 1)
	}

	return it, true
}

// replicateTo sends writes to replicas (best-effort) returning ack count.
func (dm *DistMemory) replicateTo(ctx context.Context, item *cache.Item, replicas []cluster.NodeID) int { //nolint:ireturn
	acks := 0
	for _, oid := range replicas {
		if oid == dm.localNode.ID {
			continue
		}

		if dm.transport != nil {
			err := dm.transport.ForwardSet(ctx, string(oid), item, false)
			if err == nil {
				acks++

				continue
			}

			if errors.Is(err, sentinel.ErrBackendNotFound) { // queue hint for unreachable replica
				dm.queueHint(string(oid), item)
			}
		}
	}

	return acks
}

// getWithConsistencyParallel performs parallel owner fan-out until quorum/all reached.
func (dm *DistMemory) getWithConsistencyParallel(
	ctx context.Context,
	key string,
	owners []cluster.NodeID,
) (*cache.Item, bool) { //nolint:ireturn
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
func (dm *DistMemory) queueHint(nodeID string, item *cache.Item) { // reduced complexity
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
		atomic.AddInt64(&dm.metrics.hintedGlobalDropped, 1)

		return
	}

	cloned := *item

	queue = append(queue, hintedEntry{item: &cloned, expire: time.Now().Add(dm.hintTTL), size: size})
	dm.hints[nodeID] = queue
	dm.adjustHintAccounting(1, size)
	dm.hintsMu.Unlock()

	atomic.AddInt64(&dm.metrics.hintedQueued, 1)
	atomic.StoreInt64(&dm.metrics.hintedBytes, dm.hintBytes)
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

	dm.hintStopCh = make(chan struct{})
	go dm.hintReplayLoop(ctx)
}

func (dm *DistMemory) hintReplayLoop(ctx context.Context) { //nolint:ireturn
	ticker := time.NewTicker(dm.hintReplayInt)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			dm.replayHints(ctx)
		case <-dm.hintStopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (dm *DistMemory) replayHints(ctx context.Context) { // reduced cognitive complexity
	if dm.transport == nil {
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

	atomic.StoreInt64(&dm.metrics.hintedBytes, dm.hintBytes)
	dm.hintsMu.Unlock()
}

// processHint returns 0=keep,1=remove.
func (dm *DistMemory) processHint(ctx context.Context, nodeID string, entry hintedEntry, now time.Time) int {
	if now.After(entry.expire) {
		atomic.AddInt64(&dm.metrics.hintedExpired, 1)

		return 1
	}

	err := dm.transport.ForwardSet(ctx, nodeID, entry.item, false)
	if err == nil {
		atomic.AddInt64(&dm.metrics.hintedReplayed, 1)

		return 1
	}

	if errors.Is(err, sentinel.ErrBackendNotFound) { // keep – backend still absent
		return 0
	}

	atomic.AddInt64(&dm.metrics.hintedDropped, 1)

	return 1
}

// --- Simple gossip (in-process only) ---.
func (dm *DistMemory) startGossipIfEnabled() { //nolint:ireturn
	if dm.gossipInterval <= 0 {
		return
	}

	dm.gossipStopCh = make(chan struct{})
	go dm.gossipLoop()
}

// startAutoSyncIfEnabled launches periodic merkle syncs to all other members.
func (dm *DistMemory) startAutoSyncIfEnabled(ctx context.Context) { //nolint:ireturn
	if dm.autoSyncInterval <= 0 || dm.membership == nil {
		return
	}

	if dm.autoSyncStopCh != nil { // already running
		return
	}

	dm.autoSyncStopCh = make(chan struct{})

	interval := dm.autoSyncInterval
	go dm.autoSyncLoop(ctx, interval)
}

func (dm *DistMemory) autoSyncLoop(ctx context.Context, interval time.Duration) { //nolint:ireturn
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-dm.autoSyncStopCh:
			return
		case <-ticker.C:
			dm.runAutoSyncTick(ctx)
		}
	}
}

// runAutoSyncTick performs one auto-sync cycle; separated for lower complexity.
func (dm *DistMemory) runAutoSyncTick(ctx context.Context) { //nolint:ireturn
	start := time.Now()

	var lastErr error

	members := dm.membership.List()
	limit := dm.autoSyncPeersPerInterval
	synced := 0

	for _, member := range members {
		if member == nil || string(member.ID) == dm.nodeID { // skip self
			continue
		}

		if limit > 0 && synced >= limit {
			break
		}

		err := dm.SyncWith(ctx, string(member.ID))
		if err != nil { // capture last error only
			lastErr = err
		}

		synced++
	}

	dm.lastAutoSyncDuration.Store(time.Since(start).Nanoseconds())

	if lastErr != nil {
		dm.lastAutoSyncError.Store(lastErr.Error())
	} else {
		dm.lastAutoSyncError.Store("")
	}

	atomic.AddInt64(&dm.metrics.autoSyncLoops, 1)
}

func (dm *DistMemory) gossipLoop() { //nolint:ireturn
	ticker := time.NewTicker(dm.gossipInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			dm.runGossipTick()
		case <-dm.gossipStopCh:
			return
		}
	}
}

func (dm *DistMemory) runGossipTick() { //nolint:ireturn
	if dm.membership == nil || dm.transport == nil {
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

	ip, ok := dm.transport.(*InProcessTransport)
	if !ok {
		return
	}

	remote, ok2 := ip.backends[string(target.ID)]
	if !ok2 {
		return
	}

	snapshot := dm.membership.List()
	remote.acceptGossip(snapshot)
}

func (dm *DistMemory) acceptGossip(nodes []*cluster.Node) { //nolint:ireturn
	if dm.membership == nil {
		return
	}

	for _, node := range nodes {
		if node.ID == dm.localNode.ID {
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

// chooseNewer picks the item with higher version; on version tie uses lexicographically smaller Origin as winner.
func (dm *DistMemory) chooseNewer(itemA, itemB *cache.Item) *cache.Item { //nolint:ireturn
	if itemA == nil {
		return itemB
	}

	if itemB == nil {
		return itemA
	}

	if itemB.Version > itemA.Version { // itemB newer
		atomic.AddInt64(&dm.metrics.versionConflicts, 1)

		return itemB
	}

	if itemA.Version > itemB.Version { // itemA newer
		atomic.AddInt64(&dm.metrics.versionConflicts, 1)

		return itemA
	}

	// versions equal: tie-break on origin
	if itemB.Origin < itemA.Origin { // itemB wins by tie-break
		atomic.AddInt64(&dm.metrics.versionConflicts, 1)
		atomic.AddInt64(&dm.metrics.versionTieBreaks, 1)

		return itemB
	}

	if itemA.Origin < itemB.Origin { // itemA wins by tie-break (still counts)
		atomic.AddInt64(&dm.metrics.versionConflicts, 1)
		atomic.AddInt64(&dm.metrics.versionTieBreaks, 1)
	}

	return itemA
}

// repairReplicas ensures each owner has at least the chosen version; best-effort.
func (dm *DistMemory) repairReplicas(ctx context.Context, key string, chosen *cache.Item, owners []cluster.NodeID) { //nolint:ireturn
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
		atomic.AddInt64(&dm.metrics.readRepair, 1)
	}
}

// repairRemoteReplica updates a remote replica if stale (best-effort).
func (dm *DistMemory) repairRemoteReplica(
	ctx context.Context,
	key string,
	chosen *cache.Item,
	oid cluster.NodeID,
) { // separated to reduce cyclomatic complexity //nolint:ireturn
	if dm.transport == nil { // cannot repair remote
		return
	}

	it, ok, _ := dm.transport.ForwardGet(ctx, string(oid), key)                                            //nolint:errcheck
	if !ok || it.Version < chosen.Version || (it.Version == chosen.Version && it.Origin > chosen.Origin) { // stale
		_ = dm.transport.ForwardSet(ctx, string(oid), chosen, false) //nolint:errcheck
		atomic.AddInt64(&dm.metrics.readRepair, 1)
	}
}

// handleForwardPrimary tries to forward a Set to the primary; returns (proceedAsPrimary,false) if promotion required.
func (dm *DistMemory) handleForwardPrimary(ctx context.Context, owners []cluster.NodeID, item *cache.Item) (bool, error) { //nolint:ireturn
	if dm.transport == nil {
		return false, sentinel.ErrNotOwner
	}

	atomic.AddInt64(&dm.metrics.forwardSet, 1)

	errFwd := dm.transport.ForwardSet(ctx, string(owners[0]), item, true)
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

	for _, seedAddr := range dm.seeds { // add seeds
		if seedAddr == dm.localNode.Address { // skip self
			continue
		}

		n := cluster.NewNode("", seedAddr)
		membership.Upsert(n)
	}

	dm.membership = membership
	dm.ring = ring
}

// heartbeatLoop probes peers and updates membership (best-effort experimental).
func (dm *DistMemory) heartbeatLoop(ctx context.Context) { // reduced cognitive complexity via helpers
	ticker := time.NewTicker(dm.hbInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			dm.runHeartbeatTick(ctx)
		case <-dm.stopCh:
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
	if len(owners) <= 1 || dm.transport == nil {
		return
	}

	atomic.AddInt64(&dm.metrics.replicaFanoutSet, int64(len(owners)-1))

	for _, oid := range owners[1:] { // skip primary
		if oid == dm.localNode.ID {
			continue
		}

		_ = dm.transport.ForwardSet(ctx, string(oid), item, false) //nolint:errcheck
	}
}

// recordOriginalPrimary stores first-seen primary owner for key.
func (dm *DistMemory) recordOriginalPrimary(sh *distShard, key string) { //nolint:ireturn
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
	atomic.StoreInt64(&dm.metrics.tombstonesActive, dm.countTombstones())

	if !replicate || dm.ring == nil || dm.transport == nil {
		return
	}

	owners := dm.ring.Lookup(key)
	if len(owners) <= 1 {
		return
	}

	atomic.AddInt64(&dm.metrics.replicaFanoutRemove, int64(len(owners)-1))

	for _, oid := range owners[1:] {
		if oid == dm.localNode.ID {
			continue
		}

		_ = dm.transport.ForwardRemove(ctx, string(oid), key, false) //nolint:errcheck // best-effort (tombstone inferred remotely)
	}
}

// runHeartbeatTick runs one heartbeat iteration (best-effort).
func (dm *DistMemory) runHeartbeatTick(ctx context.Context) { //nolint:ireturn
	if dm.transport == nil || dm.membership == nil {
		return
	}

	now := time.Now()

	peers := dm.membership.List()
	// optional sampling
	if dm.hbSampleSize > 0 && dm.hbSampleSize < len(peers) {
		// Fisher–Yates partial shuffle for first sampleCount elements
		sampleCount := dm.hbSampleSize
		for i := range sampleCount { // Go 1.22 int range form
			j := i + mrand.Intn(len(peers)-i) //nolint:gosec // math/rand acceptable for sampling

			peers[i], peers[j] = peers[j], peers[i]
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
func (dm *DistMemory) evaluateLiveness(ctx context.Context, now time.Time, node *cluster.Node) { //nolint:ireturn
	elapsed := now.Sub(node.LastSeen)

	if dm.hbDeadAfter > 0 && elapsed > dm.hbDeadAfter { // prune dead
		if dm.membership.Remove(node.ID) {
			atomic.AddInt64(&dm.metrics.nodesRemoved, 1)
			atomic.AddInt64(&dm.metrics.nodesDead, 1)
		}

		return
	}

	if dm.hbSuspectAfter > 0 && elapsed > dm.hbSuspectAfter && node.State == cluster.NodeAlive { // suspect
		dm.membership.Mark(node.ID, cluster.NodeSuspect)
		atomic.AddInt64(&dm.metrics.nodesSuspect, 1)
	}

	ctxHealth, cancel := context.WithTimeout(ctx, dm.hbInterval/2)
	err := dm.transport.Health(ctxHealth, string(node.ID))

	cancel()

	if err != nil {
		atomic.AddInt64(&dm.metrics.heartbeatFailure, 1)

		if node.State == cluster.NodeAlive { // escalate
			dm.membership.Mark(node.ID, cluster.NodeSuspect)
			atomic.AddInt64(&dm.metrics.nodesSuspect, 1)
		}

		return
	}

	atomic.AddInt64(&dm.metrics.heartbeatSuccess, 1)
	// Mark alive (refresh LastSeen, clear suspicion)
	dm.membership.Mark(node.ID, cluster.NodeAlive)
}
