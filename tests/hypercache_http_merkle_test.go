package tests

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/internal/cluster"
	"github.com/hyp3rd/hypercache/pkg/backend"
	cachev2 "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// TestHTTPFetchMerkle ensures HTTP transport can fetch a remote Merkle tree and SyncWith works over HTTP.
func TestHTTPFetchMerkle(t *testing.T) {
	ctx := context.Background()

	// shared ring/membership
	ring := cluster.NewRing(cluster.WithReplication(1))
	membership := cluster.NewMembership(ring)

	// create two nodes with HTTP server enabled (addresses)
	n1 := cluster.NewNode("", "127.0.0.1:9201")

	b1i, err := backend.NewDistMemory(ctx,
		backend.WithDistMembership(membership, n1),
		backend.WithDistNode("n1", "127.0.0.1:9201"),
		backend.WithDistMerkleChunkSize(2),
	)
	if err != nil {
		t.Fatalf("b1: %v", err)
	}

	n2 := cluster.NewNode("", "127.0.0.1:9202")

	b2i, err := backend.NewDistMemory(ctx,
		backend.WithDistMembership(membership, n2),
		backend.WithDistNode("n2", "127.0.0.1:9202"),
		backend.WithDistMerkleChunkSize(2),
	)
	if err != nil {
		t.Fatalf("b2: %v", err)
	}

	b1 := b1i.(*backend.DistMemory) //nolint:forcetypeassert
	b2 := b2i.(*backend.DistMemory) //nolint:forcetypeassert

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
	transport := backend.NewDistHTTPTransport(2*time.Second, resolver)
	b1.SetTransport(transport)
	b2.SetTransport(transport)

	// ensure membership has both before writes (already upserted in constructors)
	// write some keys to b1 only
	for i := range 5 { // direct inject to sidestep replication/forwarding complexity
		item := &cachev2.Item{Key: httpKey(i), Value: []byte("v"), Version: uint64(i + 1), Origin: "n1", LastUpdated: time.Now()}
		b1.DebugInject(item)
	}
	// ensure HTTP merkle endpoint reachable
	resp, err := http.Get("http://" + b1.LocalNodeAddr() + "/internal/merkle")
	if err != nil {
		t.Fatalf("merkle http get: %v", err)
	}

	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status %d", resp.StatusCode)
	}

	// b2 sync from b1 via HTTP transport
	if err := b2.SyncWith(ctx, "n1"); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// validate keys present on b2
	for i := range 5 {
		if _, ok := b2.Get(ctx, httpKey(i)); !ok {
			t.Fatalf("missing key %d post-sync", i)
		}
	}
}

func httpKey(i int) string { return "hkey:" + string(rune('a'+i)) }
