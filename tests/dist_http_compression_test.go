package tests

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// valueAsBytes normalizes a cache.Item.Value (typed any) back to a
// byte slice across the JSON wire encodings cache.Item.Value can
// arrive as: native []byte (in-process), base64-encoded string (HTTP
// JSON round-trip), or a plain string. Returns (bytes, true) when
// recognized, (nil, false) otherwise.
func valueAsBytes(v any) ([]byte, bool) {
	switch x := v.(type) {
	case []byte:
		return x, true

	case string:
		// Try base64 first — that's how []byte serializes through
		// encoding/json. Fall back to the raw string bytes for
		// values that were always-string.
		decoded, err := base64.StdEncoding.DecodeString(x)
		if err == nil {
			return decoded, true
		}

		return []byte(x), true

	default:
		return nil, false
	}
}

// newCompressionNode spins up a DistMemory configured for the
// compression round-trip test. Replication=2 with HTTP seeds means a
// Set's primary path drives `replicateTo` over the wire — that's
// where the gzip-or-not branching lives. Threshold is shared across
// the two-node cluster: server side decompresses any inbound
// `Content-Encoding: gzip` regardless of its own threshold, so the
// option is symmetric in practice.
func newCompressionNode(t *testing.T, id, addr string, seeds []string, threshold int) *backend.DistMemory {
	t.Helper()

	bi, err := backend.NewDistMemory(
		context.Background(),
		backend.WithDistNode(id, addr),
		backend.WithDistSeeds(seeds),
		backend.WithDistReplication(2),
		backend.WithDistVirtualNodes(32),
		backend.WithDistHTTPLimits(backend.DistHTTPLimits{CompressionThreshold: threshold}),
	)
	if err != nil {
		t.Fatalf("new node %s: %v", id, err)
	}

	dm, ok := bi.(*backend.DistMemory)
	if !ok {
		t.Fatalf("cast %s: %T", id, bi)
	}

	StopOnCleanup(t, dm)

	return dm
}

// waitForCompressionHealth polls /health until both nodes are
// reachable. Without this, A's Set may try to forward to B's listener
// while B is still binding under -race.
func waitForCompressionHealth(ctx context.Context, baseURL string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/health", nil)
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

// roundTripValueOnRemote performs a Set on `writer` for `key` with
// `value`, then issues a Get on `reader` and compares — covers the
// wire path on Set (writer → reader via replicateTo) and proves the
// value survived the gzip round-trip when applicable.
func roundTripValueOnRemote(
	t *testing.T,
	writer, reader *backend.DistMemory,
	key string,
	value []byte,
) {
	t.Helper()

	ctx := context.Background()

	err := writer.Set(ctx, &cache.Item{
		Key:         key,
		Value:       value,
		Version:     1,
		Origin:      string(writer.LocalNodeID()),
		LastUpdated: time.Now(),
	})
	if err != nil {
		t.Fatalf("set: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		it, ok := reader.Get(ctx, key)
		if ok && it != nil {
			got, parsed := valueAsBytes(it.Value)
			if !parsed {
				t.Fatalf("could not normalize reader value to bytes; got %T", it.Value)
			}

			if string(got) != string(value) {
				t.Fatalf("value mismatch after replication; got %d bytes want %d", len(got), len(value))
			}

			return
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("replicated value never arrived on reader")
}

// TestDistHTTP_CompressionRoundTrip is the Phase B.3 contract test:
// when CompressionThreshold is configured, large Set values gzip on
// the wire and the receiver transparently decompresses them. The
// post-replication value on the reader must equal the writer's
// original bytes, byte-for-byte.
func TestDistHTTP_CompressionRoundTrip(t *testing.T) {
	t.Parallel()

	const threshold = 256

	addrA := AllocatePort(t)
	addrB := AllocatePort(t)

	a := newCompressionNode(t, "compress-A", addrA, []string{addrB}, threshold)
	b := newCompressionNode(t, "compress-B", addrB, []string{addrA}, threshold)

	ctx := context.Background()
	for _, base := range []string{"http://" + a.LocalNodeAddr(), "http://" + b.LocalNodeAddr()} {
		if !waitForCompressionHealth(ctx, base, 5*time.Second) {
			t.Fatalf("node at %s never came up", base)
		}
	}

	// Allow ring/membership to settle so replication actually fans
	// out across both nodes.
	time.Sleep(200 * time.Millisecond)

	value := []byte(strings.Repeat("x", 4*threshold))
	roundTripValueOnRemote(t, a, b, "compressed-key", value)
}

// TestDistHTTP_CompressionDisabledRoundTrip confirms the default
// (threshold=0) wire path still works unchanged — peers without
// compression support remain compatible until operators raise the
// threshold cluster-wide.
func TestDistHTTP_CompressionDisabledRoundTrip(t *testing.T) {
	t.Parallel()

	addrA := AllocatePort(t)
	addrB := AllocatePort(t)

	a := newCompressionNode(t, "uncompress-A", addrA, []string{addrB}, 0)
	b := newCompressionNode(t, "uncompress-B", addrB, []string{addrA}, 0)

	ctx := context.Background()
	for _, base := range []string{"http://" + a.LocalNodeAddr(), "http://" + b.LocalNodeAddr()} {
		if !waitForCompressionHealth(ctx, base, 5*time.Second) {
			t.Fatalf("node at %s never came up", base)
		}
	}

	time.Sleep(200 * time.Millisecond)

	value := []byte(strings.Repeat("y", 1024))
	roundTripValueOnRemote(t, a, b, "uncompressed-key", value)
}
