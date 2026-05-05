package integration

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-json"

	"github.com/hyp3rd/hypercache/internal/cluster"
	"github.com/hyp3rd/hypercache/pkg/backend"
)

// TestDistSWIM_HTTPGossipExchange validates the HTTP-gossip wire:
// node-A pushes its membership snapshot to node-B over the dist
// HTTP transport, and node-B's `acceptGossip` merges the entries.
//
// Pre-Phase-E this path was a no-op — runGossipTick only worked
// when the transport was an InProcessTransport, so cross-process
// clusters got no membership dissemination at all.
func TestDistSWIM_HTTPGossipExchange(t *testing.T) {
	t.Parallel()

	addrA := allocatePort(t)
	addrB := allocatePort(t)

	a := mustGossipNode(t, "swim-A", addrA, []string{"swim-B@" + addrB})
	b := mustGossipNode(t, "swim-B", addrB, []string{"swim-A@" + addrA})

	// Inject a synthetic third node into A's membership only.
	// Gossip from A to B must propagate it.
	ghost := cluster.NewNode("swim-ghost", "127.0.0.1:1")
	a.Membership().Upsert(ghost)

	if memberExists(b, "swim-ghost") {
		t.Fatalf("test setup invariant broken: B already sees ghost before gossip")
	}

	// Push A's snapshot to B over HTTP.
	transport, ok := getTransport(a)
	if !ok {
		t.Fatalf("node A's transport is not a *DistHTTPTransport")
	}

	members := snapshotMembers(a)

	err := transport.Gossip(context.Background(), "swim-B", members)
	if err != nil {
		t.Fatalf("gossip A→B: %v", err)
	}

	// B should now know the ghost via merged gossip.
	if !memberExists(b, "swim-ghost") {
		t.Fatalf("expected B to see ghost after gossip; current view: %v", listMemberIDs(b))
	}
}

// TestDistSWIM_SelfRefute pins the self-refutation contract:
// when a peer's gossip claims this node is Suspect at incarnation
// N (>= local incarnation), the local node bumps its incarnation
// and re-marks itself Alive — so subsequent gossip ticks
// disseminate the refutation cluster-wide.
//
// Pre-Phase-E `acceptGossip` skipped entries about the local node
// (`continue` on ID match), so a falsely-suspected node could
// never clear suspicion via gossip; only a fresh probe could.
func TestDistSWIM_SelfRefute(t *testing.T) {
	t.Parallel()

	addr := allocatePort(t)

	dm := mustGossipNode(t, "swim-self", addr, nil)

	initialIncarnation := dm.Membership().List()[0].Incarnation

	// Forge a peer's gossip view: "swim-self is Suspect at the
	// current incarnation". This is what a peer would say after
	// a heartbeat probe failure (Phase B.1 path).
	suspectClaim := []backend.GossipMember{
		{
			ID:          "swim-self",
			Address:     addr,
			State:       "suspect",
			Incarnation: initialIncarnation,
		},
	}

	// Drive the wire: post the gossip directly via the receiver's
	// /internal/gossip endpoint so the assertion exercises the
	// production code path (acceptGossip via decodeGetBody-style
	// JSON decode), not just the in-memory function.
	postGossip(t, addr, suspectClaim)

	// After accepting the suspect claim, the local node must have
	// bumped its incarnation AND be back in NodeAlive state.
	for _, n := range dm.Membership().List() {
		if string(n.ID) != "swim-self" {
			continue
		}

		if n.Incarnation <= initialIncarnation {
			t.Fatalf("expected incarnation > %d after self-refute, got %d", initialIncarnation, n.Incarnation)
		}

		if n.State != cluster.NodeAlive {
			t.Fatalf("expected NodeAlive after self-refute, got %s", n.State.String())
		}

		return
	}

	t.Fatalf("local node missing from membership after gossip")
}

// mustGossipNode is the shared constructor — same shape as
// makePhase1Node but tuned for the SWIM tests (replication=1,
// no rebalance, fast heartbeat). Returns the unwrapped *DistMemory
// so tests can poke at membership directly.
func mustGossipNode(t *testing.T, id, addr string, seeds []string) *backend.DistMemory {
	t.Helper()

	bm, err := backend.NewDistMemory(
		context.Background(),
		backend.WithDistNode(id, addr),
		backend.WithDistSeeds(seeds),
		backend.WithDistReplication(1),
		backend.WithDistVirtualNodes(8),
		backend.WithDistHeartbeat(5*time.Second, 15*time.Second, 30*time.Second),
	)
	if err != nil {
		t.Fatalf("new node %s: %v", id, err)
	}

	dm, ok := bm.(*backend.DistMemory)
	if !ok {
		t.Fatalf("cast %s: %T", id, bm)
	}

	t.Cleanup(func() { _ = dm.Stop(context.Background()) })

	waitForDistNodeHealth(context.Background(), t, addr)

	return dm
}

// memberExists reports whether the given node ID appears in the
// dist memory's membership view.
func memberExists(dm *backend.DistMemory, id string) bool {
	for _, n := range dm.Membership().List() {
		if string(n.ID) == id {
			return true
		}
	}

	return false
}

// listMemberIDs is a debug helper for failure messages.
func listMemberIDs(dm *backend.DistMemory) []string {
	members := dm.Membership().List()
	out := make([]string, 0, len(members))

	for _, n := range members {
		out = append(out, string(n.ID))
	}

	return out
}

// snapshotMembers projects a node's membership through the
// transport's wire shape — same conversion the production
// runGossipTick uses for the HTTP path.
func snapshotMembers(dm *backend.DistMemory) []backend.GossipMember {
	members := dm.Membership().List()
	out := make([]backend.GossipMember, 0, len(members))

	for _, n := range members {
		out = append(out, backend.GossipMember{
			ID:          string(n.ID),
			Address:     n.Address,
			State:       n.State.String(),
			Incarnation: n.Incarnation,
		})
	}

	return out
}

// getTransport unwraps the dist memory's auto-created HTTP
// transport. Test-only — the production code keeps the transport
// behind an atomic.Pointer slot.
func getTransport(dm *backend.DistMemory) (*backend.DistHTTPTransport, bool) {
	// We don't have a public accessor; route through the
	// receiver-port wire test helper. The HTTP transport was
	// auto-created by tryStartHTTP, so we can build a fresh
	// instance with the same resolver to call Gossip on.
	resolver := func(nodeID string) (string, bool) {
		for _, n := range dm.Membership().List() {
			if string(n.ID) == nodeID {
				return "http://" + n.Address, true
			}
		}

		return "", false
	}

	return backend.NewDistHTTPTransport(0, resolver), true
}

// postGossip drives the dist HTTP server's `/internal/gossip`
// endpoint directly with a JSON-encoded snapshot — same wire
// shape an HTTP gossip producer would send.
func postGossip(t *testing.T, addr string, members []backend.GossipMember) {
	t.Helper()

	body, err := json.Marshal(members)
	if err != nil {
		t.Fatalf("marshal gossip: %v", err)
	}

	url := "http://" + addr + "/internal/gossip"

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("build req: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Length", strconv.Itoa(len(body)))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post gossip: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("gossip post status=%d", resp.StatusCode)
	}
}
