package integration

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/hyp3rd/ewrap"

	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// errUnexpectedStatus is the sentinel returned when the dist node health
// endpoint reports a non-OK status during the readiness poll.
var errUnexpectedStatus = ewrap.New("unexpected dist node health status")

// rebalanceTestOpts is the shared option set used by TestDistRebalanceJoin
// across all three nodes — extracted so each `mustDistNode` call site
// stays a single line.
func rebalanceTestOpts() []backend.DistMemoryOption {
	return []backend.DistMemoryOption{
		backend.WithDistReplication(2),
		backend.WithDistVirtualNodes(32),
		backend.WithDistRebalanceInterval(100 * time.Millisecond),
	}
}

// populateKeys seeds n test keys onto node's local shard via the
// DebugInject test bypass — no replication, no quorum check. We use
// the bypass deliberately: every rebalance test asserts post-state
// of the ring + migration metrics, not the pre-rebalance
// replication path. Going through Set would force a quorum write
// for every key against the brand-new two-node cluster, which is
// flake-prone (~1 in 50 runs under -shuffle in CI) when a single
// fan-out transport call misses its deadline.
//
// Tests that specifically want to exercise the replication path
// during populate should call Set directly with appropriate
// assertions; this helper is for "shape the ring, then test what
// happens next." Use populateKeysOnAll when the test needs the
// keys present on multiple nodes (e.g. replica-diff scenarios that
// assume replication has already drained).
func populateKeys(_ context.Context, t *testing.T, node *backend.DistMemory, n int) {
	t.Helper()

	for i := range n {
		k := cacheKey(i)
		it := &cache.Item{Key: k, Value: []byte("v"), Version: 1, Origin: "A", LastUpdated: time.Now()}

		node.DebugInject(it)
	}
}

// populateKeysOnAll injects n keys on every supplied node via the
// DebugInject bypass — simulates "replication has already drained
// across these nodes" without going through the quorum-write path
// that flakes under -shuffle. Use this for replica-diff and
// leave-migration tests where the post-topology assertion requires
// keys to be present on multiple nodes pre-change.
//
// Over-replication (a key landing on a node that isn't its actual
// owner per the ring) is harmless: the rebalance loop sheds keys
// from nodes that aren't owners after each tick.
func populateKeysOnAll(_ context.Context, t *testing.T, n int, nodes ...*backend.DistMemory) {
	t.Helper()

	for i := range n {
		k := cacheKey(i)
		it := &cache.Item{Key: k, Value: []byte("v"), Version: 1, Origin: "A", LastUpdated: time.Now()}

		for _, node := range nodes {
			node.DebugInject(it)
		}
	}
}

// TestDistRebalanceJoin verifies keys are migrated to a new node after join.
func TestDistRebalanceJoin(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Initial cluster: 2 nodes.
	addrA := allocatePort(t)
	addrB := allocatePort(t)

	nodeA := mustDistNode(ctx, t, "A", addrA, []string{addrB}, rebalanceTestOpts()...)
	nodeB := mustDistNode(ctx, t, "B", addrB, []string{addrA}, rebalanceTestOpts()...)

	defer func() { _ = nodeA.Stop(ctx); _ = nodeB.Stop(ctx) }()

	const totalKeys = 300

	populateKeys(ctx, t, nodeA, totalKeys)

	// Allow initial replication, then add third node C.
	time.Sleep(200 * time.Millisecond)

	skeys := sampleKeys(totalKeys)

	addrC := allocatePort(t)
	nodeC := mustDistNode(ctx, t, "C", addrC, []string{addrA, addrB}, rebalanceTestOpts()...)

	defer func() { _ = nodeC.Stop(ctx) }()

	// Manually propagate C into A and B membership (gossip-propagation-delay simulation).
	nodeA.AddPeer(addrC)
	nodeB.AddPeer(addrC)
	time.Sleep(1200 * time.Millisecond)

	postOwnedA := ownedPrimaryCount(nodeA, skeys)
	postOwnedB := ownedPrimaryCount(nodeB, skeys)
	postOwnedC := ownedPrimaryCount(nodeC, skeys)

	if postOwnedC == 0 {
		t.Fatalf("expected node C to own some keys after rebalancing")
	}

	// Distribution variance check: no node should hold > 80% of the sample.
	maxAllowed := int(float64(totalKeys) * 0.80)
	if postOwnedA > maxAllowed || postOwnedB > maxAllowed || postOwnedC > maxAllowed {
		t.Fatalf("ownership still highly skewed: A=%d B=%d C=%d", postOwnedA, postOwnedB, postOwnedC)
	}

	migrated := nodeA.Metrics().RebalancedKeys + nodeB.Metrics().RebalancedKeys + nodeC.Metrics().RebalancedKeys
	if migrated == 0 {
		t.Fatalf("expected some rebalanced keys (total migrated=0)")
	}
}

// TestDistRebalanceThrottle simulates saturation causing throttle metric increments.
func TestDistRebalanceThrottle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	addrA := allocatePort(t)
	addrB := allocatePort(t)

	// Use small batch size & low concurrency to trigger throttling when many keys.
	opts := []backend.DistMemoryOption{
		backend.WithDistReplication(2),
		backend.WithDistVirtualNodes(16),
		backend.WithDistRebalanceInterval(50 * time.Millisecond),
		backend.WithDistRebalanceBatchSize(8),
		backend.WithDistRebalanceMaxConcurrent(1),
	}

	nodeA := mustDistNode(ctx, t, "A", addrA, []string{addrB}, opts...)
	nodeB := mustDistNode(ctx, t, "B", addrB, []string{addrA}, opts...)

	defer func() { _ = nodeA.Stop(ctx); _ = nodeB.Stop(ctx) }()

	// Populate many keys on A via DebugInject (test bypass — see
	// populateKeys' doc for why). We need keys on A's primary
	// shards so the post-join rebalance has work to do; the
	// pre-join replication path is not under test here.
	populateKeys(ctx, t, nodeA, 400)

	// Add third node to force migrations while concurrency=1, which should queue batches.
	addrC := allocatePort(t)

	nodeC := mustDistNode(ctx, t, "C", addrC, []string{addrA, addrB}, opts...)
	defer func() { _ = nodeC.Stop(ctx) }()

	// propagate membership like in join test
	nodeA.AddPeer(addrC)
	nodeB.AddPeer(addrC)

	time.Sleep(1500 * time.Millisecond)

	// Expect throttle metric > 0 on some node (A likely source).
	if a, b, c := nodeA.Metrics().RebalanceThrottle, nodeB.Metrics().RebalanceThrottle, nodeC.Metrics().RebalanceThrottle; a == 0 &&
		b == 0 &&
		c == 0 {
		t.Fatalf("expected throttle metric to increment (a=%d b=%d c=%d)", a, b, c)
	}
}

// Helpers.

func mustDistNode(
	ctx context.Context,
	t *testing.T,
	id, addr string,
	seeds []string,
	extra ...backend.DistMemoryOption,
) *backend.DistMemory {
	t.Helper()

	opts := []backend.DistMemoryOption{ //nolint:prealloc // literal helper-builder list, append-once with extra below
		backend.WithDistNode(id, addr),
		backend.WithDistSeeds(seeds),
		backend.WithDistHintReplayInterval(200 * time.Millisecond),
		backend.WithDistHintTTL(5 * time.Second),
		backend.WithDistReadConsistency(backend.ConsistencyQuorum),
		backend.WithDistWriteConsistency(backend.ConsistencyQuorum),
	}

	opts = append(opts, extra...)

	bm, err := backend.NewDistMemory(ctx, opts...)
	if err != nil {
		t.Fatalf("new dist memory: %v", err)
	}

	waitForDistNodeHealth(ctx, t, addr)

	bk, ok := bm.(*backend.DistMemory)
	if !ok {
		t.Fatalf("expected *backend.DistMemory, got %T", bm)
	}

	return bk
}

func cacheKey(i int) string { return "k" + strconv.Itoa(i) }

func sampleKeys(n int) []string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, cacheKey(i))
	}

	return out
}

func ownedPrimaryCount(dm *backend.DistMemory, keys []string) int {
	if dm == nil || dm.Membership() == nil {
		return 0
	}

	ring := dm.Membership().Ring()
	if ring == nil {
		return 0
	}

	c := 0

	self := dm.LocalNodeID()
	for _, k := range keys {
		owners := ring.Lookup(k)
		if len(owners) > 0 && owners[0] == self {
			c++
		}
	}

	return c
}

func waitForDistNodeHealth(ctx context.Context, t *testing.T, addr string) {
	t.Helper()

	client := &http.Client{Timeout: 100 * time.Millisecond}
	healthURL := "http://" + addr + "/health"
	deadline := time.Now().Add(3 * time.Second)

	var lastErr error

	for time.Now().Before(deadline) {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
		if reqErr != nil {
			t.Fatalf("build health request: %v", reqErr)
		}

		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				return
			}

			lastErr = fmt.Errorf("%w: %d", errUnexpectedStatus, resp.StatusCode)
		} else {
			lastErr = err
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("node %s health not ready: %v", addr, lastErr)
}
