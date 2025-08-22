package backend

import (
	"context"
	"hash/fnv"
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
	// versionClock     uint64 // lamport-like counter for primary writes
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
func NewDistMemory(opts ...DistMemoryOption) (IBackend[DistMemory], error) {
	dm := &DistMemory{shardCount: defaultDistShardCount, replication: 1, readConsistency: ConsistencyOne, writeConsistency: ConsistencyQuorum}
	for _, opt := range opts {
		opt(dm)
	}

	if dm.shardCount <= 0 {
		dm.shardCount = defaultDistShardCount
	}

	for range dm.shardCount { // Go 1.22+ range-over-int
		dm.shards = append(dm.shards, &distShard{items: cache.New()})
	}

	if dm.membership == nil {
		dm.initStandaloneMembership()
	} else {
		if dm.localNode == nil {
			dm.localNode = cluster.NewNode("", "local")
		}

		dm.membership.Upsert(dm.localNode)
		dm.ring = dm.membership.Ring()
	}

	if dm.hbInterval > 0 && dm.transport != nil {
		dm.stopCh = make(chan struct{})
		go dm.heartbeatLoop()
	}

	return dm, nil
}

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

	return dm.getWithConsistency(ctx, key, owners)
}

// Set stores item.
func (dm *DistMemory) Set(_ context.Context, item *cache.Item) error { //nolint:ireturn
	err := item.Valid()
	if err != nil {
		return err
	}

	owners := dm.lookupOwners(item.Key)
	if len(owners) == 0 {
		return sentinel.ErrNotOwner
	}

	if owners[0] != dm.localNode.ID { // forward to primary
		if dm.transport == nil {
			return sentinel.ErrNotOwner
		}

		atomic.AddInt64(&dm.metrics.forwardSet, 1)

		return dm.transport.ForwardSet(string(owners[0]), item, true)
	}

	// primary path
	dm.applySet(item, false)

	acks := 1 + dm.replicateTo(item, owners[1:])

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
func (dm *DistMemory) Remove(_ context.Context, keys ...string) error { //nolint:ireturn
	for _, key := range keys {
		if dm.isOwner(key) { // primary path
			dm.applyRemove(key, true)

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
		_ = dm.transport.ForwardRemove(string(owners[0]), key, true) //nolint:errcheck // best-effort
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
	ForwardSet(nodeID string, item *cache.Item, replicate bool) error
	ForwardGet(ctx context.Context, nodeID string, key string) (*cache.Item, bool, error)
	ForwardRemove(nodeID string, key string, replicate bool) error
	Health(ctx context.Context, nodeID string) error
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
func (t *InProcessTransport) ForwardSet(nodeID string, item *cache.Item, replicate bool) error { //nolint:ireturn
	b, ok := t.backends[nodeID]
	if !ok {
		return sentinel.ErrBackendNotFound
	}
	// direct apply bypasses ownership check (already routed)
	b.applySet(item, replicate)

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
func (t *InProcessTransport) ForwardRemove(nodeID string, key string, replicate bool) error { //nolint:ireturn
	b, ok := t.backends[nodeID]
	if !ok {
		return sentinel.ErrBackendNotFound
	}

	b.applyRemove(key, replicate)

	return nil
}

// Health implements DistTransport.Health for in-process transport (always healthy if registered).
func (t *InProcessTransport) Health(_ context.Context, nodeID string) error { //nolint:ireturn
	if _, ok := t.backends[nodeID]; !ok {
		return sentinel.ErrBackendNotFound
	}

	return nil
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
	}
}

// Stop stops heartbeat loop if running.
func (dm *DistMemory) Stop(_ context.Context) error { //nolint:ireturn
	if dm.stopCh != nil {
		close(dm.stopCh)
	}

	return nil
}

// lookupOwners returns ring owners slice for a key (nil if no ring).
func (dm *DistMemory) lookupOwners(key string) []cluster.NodeID { //nolint:ireturn
	if dm.ring == nil {
		return nil
	}

	return dm.ring.Lookup(key)
}

// requiredAcks computes required acknowledgements for given consistency level.
func (dm *DistMemory) requiredAcks(total int, lvl ConsistencyLevel) int { //nolint:ireturn
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
	primary := owners[0]
	if primary == dm.localNode.ID && len(owners) > 1 {
		primary = owners[1]
	}

	if primary == dm.localNode.ID { // local retry
		if it, ok := dm.shardFor(key).items.Get(key); ok {
			return it, true
		}

		return nil, false
	}

	atomic.AddInt64(&dm.metrics.forwardGet, 1)

	it, ok, _ := dm.transport.ForwardGet(ctx, string(primary), key) //nolint:errcheck
	if !ok {
		return nil, false
	}

	if dm.isOwner(key) { // read-repair
		if _, ok2 := dm.shardFor(key).items.Get(key); !ok2 {
			cloned := *it
			dm.applySet(&cloned, false)
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

	for _, oid := range owners {
		if oid == dm.localNode.ID {
			if it, ok := dm.shardFor(key).items.Get(key); ok {
				if chosen == nil {
					chosen = it
				}

				acks++
			}

			continue
		}

		it, ok, _ := dm.transport.ForwardGet(ctx, string(oid), key) //nolint:errcheck
		if ok {
			if chosen == nil {
				chosen = it
			}

			acks++
		}
	}

	if acks < needed || chosen == nil {
		return nil, false
	}

	if dm.isOwner(key) { // read-repair if missing
		if _, ok := dm.shardFor(key).items.Get(key); !ok {
			cloned := *chosen
			dm.applySet(&cloned, false)
			atomic.AddInt64(&dm.metrics.readRepair, 1)
		}
	}

	return chosen, true
}

// replicateTo sends writes to replicas (best-effort) returning ack count.
func (dm *DistMemory) replicateTo(item *cache.Item, replicas []cluster.NodeID) int { //nolint:ireturn
	acks := 0

	for _, oid := range replicas {
		if oid == dm.localNode.ID {
			continue
		}

		if dm.transport != nil && dm.transport.ForwardSet(string(oid), item, false) == nil {
			acks++
		}
	}

	return acks
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
func (dm *DistMemory) heartbeatLoop() { // reduced cognitive complexity via helpers
	ticker := time.NewTicker(dm.hbInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			dm.runHeartbeatTick()
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
func (dm *DistMemory) applySet(item *cache.Item, replicate bool) {
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

		_ = dm.transport.ForwardSet(string(oid), item, false) //nolint:errcheck // best-effort replica write
	}
}

// applyRemove deletes locally and optionally fan-outs removal to replicas.
func (dm *DistMemory) applyRemove(key string, replicate bool) {
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

		_ = dm.transport.ForwardRemove(string(oid), key, false) //nolint:errcheck // best-effort
	}
}

// runHeartbeatTick runs one heartbeat iteration (best-effort).
func (dm *DistMemory) runHeartbeatTick() { //nolint:ireturn
	if dm.transport == nil || dm.membership == nil {
		return
	}

	now := time.Now()

	peers := dm.membership.List()
	for _, node := range peers { // rename for clarity
		if node.ID == dm.localNode.ID {
			continue
		}

		dm.evaluateLiveness(now, node)
	}
}

// evaluateLiveness applies timeout-based transitions then performs a probe.
func (dm *DistMemory) evaluateLiveness(now time.Time, node *cluster.Node) { //nolint:ireturn
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

	ctx, cancel := context.WithTimeout(context.Background(), dm.hbInterval/2)
	err := dm.transport.Health(ctx, string(node.ID))

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
