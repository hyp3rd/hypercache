package tests

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/internal/cluster"
	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// newHTTPMerkleNode constructs a DistMemory backend with an HTTP server
// listening on the supplied address — used by the HTTP-transport merkle
// tests where in-process transport isn't enough.
//
// Construction uses context.Background() rather than a caller-supplied ctx
// because Stop runs from t.Cleanup at end-of-test, where the test ctx may
// already be canceled — propagating the same ctx into Stop would leak the
// HTTP listener. See StopOnCleanup for the same rationale.
func newHTTPMerkleNode(t *testing.T, membership *cluster.Membership, id, addr string) *backend.DistMemory {
	t.Helper()

	node := cluster.NewNode("", addr)

	bi, err := backend.NewDistMemory(
		context.Background(),
		backend.WithDistMembership(membership, node),
		backend.WithDistNode(id, addr),
		backend.WithDistMerkleChunkSize(2),
	)
	if err != nil {
		t.Fatalf("new %s: %v", id, err)
	}

	b, ok := bi.(*backend.DistMemory)
	if !ok {
		t.Fatalf("expected *backend.DistMemory, got %T", bi)
	}

	StopOnCleanup(t, b)

	return b
}

// waitForMerkleEndpoint polls a node's /internal/merkle HTTP endpoint until
// it responds 200. Under -race the fiber listener can take seconds to start
// accepting after Listen() returns, so a one-shot Get would race.
func waitForMerkleEndpoint(ctx context.Context, baseURL string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/internal/merkle", nil)
		if err != nil {
			return false
		}

		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				return true
			}
		}

		time.Sleep(50 * time.Millisecond)
	}

	return false
}

// TestHTTPFetchMerkle ensures HTTP transport can fetch a remote Merkle tree and SyncWith works over HTTP.
func TestHTTPFetchMerkle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	ring := cluster.NewRing(cluster.WithReplication(1))
	membership := cluster.NewMembership(ring)

	addr1 := AllocatePort(t)
	addr2 := AllocatePort(t)

	b1 := newHTTPMerkleNode(t, membership, "n1", addr1)
	b2 := newHTTPMerkleNode(t, membership, "n2", addr2)

	resolver := func(id string) (string, bool) {
		switch id {
		case "n1":
			return "http://" + b1.LocalNodeAddr(), true
		case "n2":
			return "http://" + b2.LocalNodeAddr(), true
		}

		return "", false
	}

	// 5s transport timeout (was 2s) — under -race the fiber listener can take
	// >2s to accept its first request, which made SyncWith time out spuriously.
	transport := backend.NewDistHTTPTransport(5*time.Second, resolver)
	b1.SetTransport(transport)
	b2.SetTransport(transport)

	// Direct inject on b1 to sidestep replication/forwarding complexity.
	for i := range 5 {
		item := &cache.Item{Key: httpKey(i), Value: []byte("v"), Version: uint64(i + 1), Origin: "n1", LastUpdated: time.Now()}
		b1.DebugInject(item)
	}

	if !waitForMerkleEndpoint(ctx, "http://"+b1.LocalNodeAddr(), 10*time.Second) {
		t.Fatal("merkle endpoint did not become ready within deadline")
	}

	syncErr := b2.SyncWith(ctx, "n1")
	if syncErr != nil {
		t.Fatalf("sync: %v", syncErr)
	}

	// Validate keys present on b2. Each missing key is retried once to absorb
	// any async tail in sync's apply path.
	for i := range 5 {
		if _, ok := b2.Get(ctx, httpKey(i)); ok {
			continue
		}

		err := b2.SyncWith(ctx, "n1")
		if err != nil {
			t.Fatalf("re-sync: %v", err)
		}

		if _, ok := b2.Get(ctx, httpKey(i)); !ok {
			t.Fatalf("missing key %d post-sync", i)
		}
	}
}

func httpKey(i int) string {
	return "hkey:" + string(rune('a'+i)) //nolint:gosec // test fixture, i bounded by 5 in caller
}
