package backend

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"hash"
	"hash/fnv"
	"math/big"
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

	// consistency / versioning (initial)
	readConsistency  ConsistencyLevel
	writeConsistency ConsistencyLevel
	versionCounter   uint64 // global monotonic for this node (lamport-like)

	// hinted handoff
	hintTTL        time.Duration
	hintReplayInt  time.Duration
	hintMaxPerNode int
	hintsMu        sync.Mutex
	hints          map[string][]hintedEntry // nodeID -> queue
	hintStopCh     chan struct{}

	// parallel reads
	parallelReads bool

	// simple gossip
	gossipInterval time.Duration
	gossipStopCh   chan struct{}

	// anti-entropy
	merkleChunkSize int // number of keys per leaf chunk (power-of-two recommended)
}

// hintedEntry represents a deferred replica write.
type hintedEntry struct {
	item   *cache.Item
	expire time.Time
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

// Membership returns current membership reference (read-only usage).
func (dm *DistMemory) Membership() *cluster.Membership { return dm.membership }

// Ring returns the ring reference.
func (dm *DistMemory) Ring() *cluster.Ring { return dm.ring }

type distShard struct {
	items cache.ConcurrentMap
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
		end := i + chunkSize
		if end > len(entries) {
			end = len(entries)
		}

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

	remoteTree, err := dm.transport.FetchMerkle(ctx, nodeID)
	if err != nil {
		return err
	}

	localTree := dm.BuildMerkleTree()

	diffs := localTree.DiffLeafRanges(remoteTree)
	if len(diffs) == 0 { // already consistent
		return nil
	}

	// Collect and sort local entries for chunk boundary mapping.
	entries := dm.merkleEntries()
	sort.Slice(entries, func(i, j int) bool { return entries[i].k < entries[j].k })

	// Enumerate remote keys for missing detection when using in-process transport.
	missingKeys := dm.enumerateRemoteOnlyKeys(nodeID, entries)

	chunkSize := localTree.ChunkSize
	for _, ci := range diffs {
		start := ci * chunkSize
		if start >= len(entries) { // diff chunk beyond local entries (only missing keys handled later)
			continue
		}

		end := start + chunkSize
		if end > len(entries) {
			end = len(entries)
		}

		for _, e := range entries[start:end] {
			dm.fetchAndAdopt(ctx, nodeID, e.k)
		}
	}
	// fetch keys that exist only remotely
	for k := range missingKeys {
		dm.fetchAndAdopt(ctx, nodeID, k)
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
	dm := &DistMemory{shardCount: defaultDistShardCount, replication: 1, readConsistency: ConsistencyOne, writeConsistency: ConsistencyQuorum}
	for _, opt := range opts {
		opt(dm)
	}

	dm.ensureShardConfig()
	dm.initMembershipIfNeeded()
	dm.tryStartHTTP(ctx)
	dm.startHeartbeatIfEnabled(ctx)
	dm.startHintReplayIfEnabled(ctx)
	dm.startGossipIfEnabled()

	return dm, nil
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

	needed := dm.requiredAcks(len(owners), dm.writeConsistency)
	if acks < needed {
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
	for _, key := range keys {
		if dm.isOwner(key) { // primary path
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

	dm.shardFor(it.Key).items.Set(it.Key, it)
}

// LocalNodeID returns this instance's node ID (testing helper).
func (dm *DistMemory) LocalNodeID() cluster.NodeID { return dm.localNode.ID }

// DebugOwners returns current owners slice for a key (for tests).
func (dm *DistMemory) DebugOwners(key string) []cluster.NodeID {
	if dm.ring == nil {
		return nil
	}

	return dm.ring.Lookup(key)
}

// DistTransport defines forwarding operations needed by DistMemory.
type DistTransport interface {
	ForwardSet(ctx context.Context, nodeID string, item *cache.Item, replicate bool) error
	ForwardGet(ctx context.Context, nodeID string, key string) (*cache.Item, bool, error)
	ForwardRemove(ctx context.Context, nodeID string, key string, replicate bool) error
	Health(ctx context.Context, nodeID string) error
	FetchMerkle(ctx context.Context, nodeID string) (*MerkleTree, error)
}

// InProcessTransport implements DistTransport for multiple DistMemory instances in the same process.
type InProcessTransport struct{ backends map[string]*DistMemory }

// NewInProcessTransport creates a new empty transport.
func NewInProcessTransport() *InProcessTransport {
	return &InProcessTransport{backends: map[string]*DistMemory{}}
}

// Register adds backends; safe to call multiple times.
func (t *InProcessTransport) Register(b *DistMemory) {
	if b != nil {
		t.backends[string(b.localNode.ID)] = b
	}
}

// Unregister removes a backend (simulate failure in tests).
func (t *InProcessTransport) Unregister(id string) { delete(t.backends, id) }

// ForwardSet forwards a set operation to the specified backend node.
func (t *InProcessTransport) ForwardSet(ctx context.Context, nodeID string, item *cache.Item, replicate bool) error { //nolint:ireturn
	b, ok := t.backends[nodeID]
	if !ok {
		return sentinel.ErrBackendNotFound
	}
	// direct apply bypasses ownership check (already routed)
	b.applySet(ctx, item, replicate)

	return nil
}

// ForwardGet forwards a get operation to the specified backend node.
func (t *InProcessTransport) ForwardGet(_ context.Context, nodeID string, key string) (*cache.Item, bool, error) { //nolint:ireturn
	b, ok := t.backends[nodeID]
	if !ok {
		return nil, false, sentinel.ErrBackendNotFound
	}

	it, ok2 := b.shardFor(key).items.Get(key)
	if !ok2 {
		return nil, false, nil
	}

	return it, true, nil
}

// ForwardRemove forwards a remove operation to the specified backend node.
func (t *InProcessTransport) ForwardRemove(ctx context.Context, nodeID string, key string, replicate bool) error { //nolint:ireturn
	b, ok := t.backends[nodeID]
	if !ok {
		return sentinel.ErrBackendNotFound
	}

	b.applyRemove(ctx, key, replicate)

	return nil
}

// Health implements DistTransport.Health for in-process transport (always healthy if registered).
func (t *InProcessTransport) Health(_ context.Context, nodeID string) error { //nolint:ireturn
	if _, ok := t.backends[nodeID]; !ok {
		return sentinel.ErrBackendNotFound
	}

	return nil
}

// FetchMerkle returns a snapshot Merkle tree of the target backend.
func (t *InProcessTransport) FetchMerkle(_ context.Context, nodeID string) (*MerkleTree, error) { //nolint:ireturn
	b, ok := t.backends[nodeID]
	if !ok {
		return nil, sentinel.ErrBackendNotFound
	}

	return b.BuildMerkleTree(), nil
}

// distMetrics holds internal counters (best-effort, not atomic snapshot consistent).
type distMetrics struct {
	forwardGet          int64
	forwardSet          int64
	forwardRemove       int64
	replicaFanoutSet    int64
	replicaFanoutRemove int64
	readRepair          int64
	replicaGetMiss      int64
	heartbeatSuccess    int64
	heartbeatFailure    int64
	nodesRemoved        int64
	versionConflicts    int64 // times a newer version (or tie-broken origin) replaced previous candidate
	versionTieBreaks    int64 // subset of conflicts decided by origin tie-break
	readPrimaryPromote  int64 // times read path skipped unreachable primary and promoted next owner
	hintedQueued        int64 // hints queued
	hintedReplayed      int64 // hints successfully replayed
	hintedExpired       int64 // hints expired before delivery
	hintedDropped       int64 // hints dropped due to non-not-found transport errors
	merkleSyncs         int64 // merkle sync operations completed
	merkleKeysPulled    int64 // keys applied during sync
}

// DistMetrics snapshot.
type DistMetrics struct {
	ForwardGet          int64
	ForwardSet          int64
	ForwardRemove       int64
	ReplicaFanoutSet    int64
	ReplicaFanoutRemove int64
	ReadRepair          int64
	ReplicaGetMiss      int64
	HeartbeatSuccess    int64
	HeartbeatFailure    int64
	NodesRemoved        int64
	VersionConflicts    int64
	VersionTieBreaks    int64
	ReadPrimaryPromote  int64
	HintedQueued        int64
	HintedReplayed      int64
	HintedExpired       int64
	HintedDropped       int64
	MerkleSyncs         int64
	MerkleKeysPulled    int64
}

// Metrics returns a snapshot of distributed metrics.
func (dm *DistMemory) Metrics() DistMetrics {
	return DistMetrics{
		ForwardGet:          atomic.LoadInt64(&dm.metrics.forwardGet),
		ForwardSet:          atomic.LoadInt64(&dm.metrics.forwardSet),
		ForwardRemove:       atomic.LoadInt64(&dm.metrics.forwardRemove),
		ReplicaFanoutSet:    atomic.LoadInt64(&dm.metrics.replicaFanoutSet),
		ReplicaFanoutRemove: atomic.LoadInt64(&dm.metrics.replicaFanoutRemove),
		ReadRepair:          atomic.LoadInt64(&dm.metrics.readRepair),
		ReplicaGetMiss:      atomic.LoadInt64(&dm.metrics.replicaGetMiss),
		HeartbeatSuccess:    atomic.LoadInt64(&dm.metrics.heartbeatSuccess),
		HeartbeatFailure:    atomic.LoadInt64(&dm.metrics.heartbeatFailure),
		NodesRemoved:        atomic.LoadInt64(&dm.metrics.nodesRemoved),
		VersionConflicts:    atomic.LoadInt64(&dm.metrics.versionConflicts),
		VersionTieBreaks:    atomic.LoadInt64(&dm.metrics.versionTieBreaks),
		ReadPrimaryPromote:  atomic.LoadInt64(&dm.metrics.readPrimaryPromote),
		HintedQueued:        atomic.LoadInt64(&dm.metrics.hintedQueued),
		HintedReplayed:      atomic.LoadInt64(&dm.metrics.hintedReplayed),
		HintedExpired:       atomic.LoadInt64(&dm.metrics.hintedExpired),
		HintedDropped:       atomic.LoadInt64(&dm.metrics.hintedDropped),
		MerkleSyncs:         atomic.LoadInt64(&dm.metrics.merkleSyncs),
		MerkleKeysPulled:    atomic.LoadInt64(&dm.metrics.merkleKeysPulled),
	}
}

// Stop stops heartbeat loop if running.
func (dm *DistMemory) Stop(ctx context.Context) error { //nolint:ireturn
	if dm.stopCh != nil {
		close(dm.stopCh)
	}

	if dm.hintStopCh != nil {
		close(dm.hintStopCh)
	}

	if dm.gossipStopCh != nil {
		close(dm.gossipStopCh)
	}

	if dm.httpServer != nil {
		err := dm.httpServer.stop(ctx) // best-effort
		if err != nil {
			return err
		}
	}

	return nil
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
	if gerr != nil || !ok {
		return
	}

	if cur, okLocal := dm.shardFor(key).items.Get(key); !okLocal || it.Version > cur.Version {
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
	}

	return entries
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

	for range dm.shardCount {
		dm.shards = append(dm.shards, &distShard{items: cache.New()})
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
	switch lvl {
	case ConsistencyAll:
		return total
	case ConsistencyQuorum:
		return (total / 2) + 1
	case ConsistencyOne:
		return 1
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
	if dm.isOwner(key) {
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

	for idx, oid := range owners {
		it, ok := dm.fetchOwner(ctx, key, idx, oid)
		if !ok {
			continue
		}

		chosen = dm.chooseNewer(chosen, it)
		acks++
	}

	if acks < needed || chosen == nil {
		return nil, false
	}

	// version-based read repair across all owners if stale/missing
	dm.repairReplicas(ctx, key, chosen, owners)

	return chosen, true
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
func (dm *DistMemory) getWithConsistencyParallel(ctx context.Context, key string, owners []cluster.NodeID) (*cache.Item, bool) { //nolint:ireturn
	needed := dm.requiredAcks(len(owners), dm.readConsistency)

	type res struct {
		it *cache.Item
		ok bool
	}

	ch := make(chan res, len(owners))

	ctxFetch, cancel := context.WithCancel(ctx)
	defer cancel()

	for idx, oid := range owners { // launch all
		go func() {
			it, ok := dm.fetchOwner(ctxFetch, key, idx, oid)
			ch <- res{it: it, ok: ok}
		}()
	}

	acks := 0

	var chosen *cache.Item
	for range owners {
		r := <-ch
		if r.ok {
			chosen = dm.chooseNewer(chosen, r.it)

			acks++
			if acks >= needed && chosen != nil { // early satisfied
				cancel()

				break
			}
		}
	}

	if acks < needed || chosen == nil {
		return nil, false
	}

	dm.repairReplicas(ctx, key, chosen, owners)

	return chosen, true
}

// --- Hinted handoff implementation ---.
func (dm *DistMemory) queueHint(nodeID string, item *cache.Item) {
	if dm.hintTTL <= 0 { // disabled
		return
	}

	dm.hintsMu.Lock()

	if dm.hints == nil {
		dm.hints = make(map[string][]hintedEntry)
	}

	queueHints := dm.hints[nodeID]
	if dm.hintMaxPerNode > 0 && len(queueHints) >= dm.hintMaxPerNode { // drop oldest
		queueHints = queueHints[1:]
	}

	cloned := *item

	queueHints = append(queueHints, hintedEntry{item: &cloned, expire: time.Now().Add(dm.hintTTL)})
	dm.hints[nodeID] = queueHints
	dm.hintsMu.Unlock()
	atomic.AddInt64(&dm.metrics.hintedQueued, 1)
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

func (dm *DistMemory) replayHints(ctx context.Context) { //nolint:ireturn
	if dm.transport == nil {
		return
	}

	now := time.Now()

	dm.hintsMu.Lock()

	for nodeID, q := range dm.hints {
		out := q[:0]
		for _, hint := range q {
			if now.After(hint.expire) { // expired
				atomic.AddInt64(&dm.metrics.hintedExpired, 1)

				continue
			}

			err := dm.transport.ForwardSet(ctx, nodeID, hint.item, false) // best-effort
			if err == nil {                                               // success
				atomic.AddInt64(&dm.metrics.hintedReplayed, 1)

				continue
			}

			if errors.Is(err, sentinel.ErrBackendNotFound) { // keep
				out = append(out, hint)

				continue
			}

			atomic.AddInt64(&dm.metrics.hintedDropped, 1)
		}

		if len(out) == 0 {
			delete(dm.hints, nodeID)
		} else {
			dm.hints[nodeID] = out
		}
	}

	dm.hintsMu.Unlock()
}

// --- Simple gossip (in-process only) ---.
func (dm *DistMemory) startGossipIfEnabled() { //nolint:ireturn
	if dm.gossipInterval <= 0 {
		return
	}

	dm.gossipStopCh = make(chan struct{})
	go dm.gossipLoop()
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
				if !dm.isOwner(item.Key) { // still not recognized locally (ring maybe outdated)
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
func (dm *DistMemory) isOwner(key string) bool {
	if dm.ring == nil { // treat nil ring as local-only mode
		return true
	}

	owners := dm.ring.Lookup(key)
	for _, id := range owners {
		if id == dm.localNode.ID {
			return true
		}
	}

	return len(owners) == 0 // empty ring => owner
}

// applySet stores item locally and optionally replicates to other owners.
// replicate indicates whether replication fan-out should occur (false for replica writes).
func (dm *DistMemory) applySet(ctx context.Context, item *cache.Item, replicate bool) {
	dm.shardFor(item.Key).items.Set(item.Key, item)

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

		_ = dm.transport.ForwardSet(ctx, string(oid), item, false) //nolint:errcheck // best-effort replica write
	}
}

// applyRemove deletes locally and optionally fan-outs removal to replicas.
func (dm *DistMemory) applyRemove(ctx context.Context, key string, replicate bool) {
	dm.shardFor(key).items.Remove(key)

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

		_ = dm.transport.ForwardRemove(ctx, string(oid), key, false) //nolint:errcheck // best-effort
	}
}

// runHeartbeatTick runs one heartbeat iteration (best-effort).
func (dm *DistMemory) runHeartbeatTick(ctx context.Context) { //nolint:ireturn
	if dm.transport == nil || dm.membership == nil {
		return
	}

	now := time.Now()

	peers := dm.membership.List()
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
		}

		return
	}

	if dm.hbSuspectAfter > 0 && elapsed > dm.hbSuspectAfter && node.State == cluster.NodeAlive { // suspect
		dm.membership.Mark(node.ID, cluster.NodeSuspect)
	}

	ctxHealth, cancel := context.WithTimeout(ctx, dm.hbInterval/2)
	err := dm.transport.Health(ctxHealth, string(node.ID))

	cancel()

	if err != nil {
		atomic.AddInt64(&dm.metrics.heartbeatFailure, 1)

		if node.State == cluster.NodeAlive { // escalate
			dm.membership.Mark(node.ID, cluster.NodeSuspect)
		}

		return
	}

	atomic.AddInt64(&dm.metrics.heartbeatSuccess, 1)
	// Mark alive (refresh LastSeen, clear suspicion)
	dm.membership.Mark(node.ID, cluster.NodeAlive)
}
