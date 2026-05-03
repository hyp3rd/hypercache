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

// TestHTTPFetchMerkle ensures HTTP transport can fetch a remote Merkle tree and SyncWith works over HTTP.
func TestHTTPFetchMerkle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// shared ring/membership
	ring := cluster.NewRing(cluster.WithReplication(1))
	membership := cluster.NewMembership(ring)

	// create two nodes with HTTP server enabled (dynamically allocated addresses)
	addr1 := AllocatePort(t)
	addr2 := AllocatePort(t)

	n1 := cluster.NewNode("", addr1)

	b1i, err := backend.NewDistMemory(ctx,
		backend.WithDistMembership(membership, n1),
		backend.WithDistNode("n1", addr1),
		backend.WithDistMerkleChunkSize(2),
	)
	if err != nil {
		t.Fatalf("b1: %v", err)
	}

	n2 := cluster.NewNode("", addr2)

	b2i, err := backend.NewDistMemory(ctx,
		backend.WithDistMembership(membership, n2),
		backend.WithDistNode("n2", addr2),
		backend.WithDistMerkleChunkSize(2),
	)
	if err != nil {
		t.Fatalf("b2: %v", err)
	}

	b1, ok := b1i.(*backend.DistMemory)
	if !ok {
		t.Fatalf("failed to cast b1i to *backend.DistMemory")
	}

	b2, ok := b2i.(*backend.DistMemory)
	if !ok {
		t.Fatalf("failed to cast b2i to *backend.DistMemory")
	}

	StopOnCleanup(t, b1)
	StopOnCleanup(t, b2)

	// HTTP transport resolver maps node IDs to http base URLs.
	resolver := func(id string) (string, bool) {
		switch id { // node IDs same as provided
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

	// ensure membership has both before writes (already upserted in constructors)
	// write some keys to b1 only
	for i := range 5 { // direct inject to sidestep replication/forwarding complexity
		item := &cache.Item{Key: httpKey(i), Value: []byte("v"), Version: uint64(i + 1), Origin: "n1", LastUpdated: time.Now()}
		b1.DebugInject(item)
	}

	// Poll the HTTP merkle endpoint until it actually responds 200. Under
	// -race the fiber listener can take seconds to start accepting requests
	// even after Listen() returns; a single-shot Get is racy.
	merkleReady := false

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+b1.LocalNodeAddr()+"/internal/merkle", nil)
		if reqErr != nil {
			t.Fatalf("build merkle request: %v", reqErr)
		}

		resp, getErr := http.DefaultClient.Do(req)
		if getErr == nil {
			_ = resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				merkleReady = true

				break
			}
		}

		time.Sleep(50 * time.Millisecond)
	}

	if !merkleReady {
		t.Fatal("merkle endpoint did not become ready within deadline")
	}

	// b2 sync from b1 via HTTP transport
	if err := b2.SyncWith(ctx, "n1"); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// Validate keys present on b2. Allow brief retry to absorb any async tail
	// in sync's apply path (each missing key is retried once).
	for i := range 5 {
		if _, ok := b2.Get(ctx, httpKey(i)); ok {
			continue
		}

		// One retry: re-sync and check again.
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
