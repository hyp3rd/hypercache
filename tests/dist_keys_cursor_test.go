package tests

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/goccy/go-json"

	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// keysPage matches the JSON shape returned by /internal/keys.
type keysPage struct {
	Keys       []string `json:"keys"`
	NextCursor string   `json:"next_cursor"`
	Truncated  bool     `json:"truncated"`
}

// fetchKeysPage issues `GET /internal/keys?cursor=<cursor>` (or
// without cursor when empty) and decodes the response. Used by the
// pagination tests below to walk the cursor chain. Auth is not
// configured in these tests so no Authorization header is set.
func fetchKeysPage(ctx context.Context, t *testing.T, baseURL, cursor string, limit int) keysPage {
	t.Helper()

	url := baseURL + "/internal/keys"

	first := true

	if cursor != "" {
		url += "?cursor=" + cursor

		first = false
	}

	if limit > 0 {
		sep := "?"
		if !first {
			sep = "&"
		}

		url += sep + "limit=" + strconv.Itoa(limit)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status %d for %s", resp.StatusCode, url)
	}

	var page keysPage

	decodeErr := json.NewDecoder(resp.Body).Decode(&page)
	if decodeErr != nil {
		t.Fatalf("decode page: %v", decodeErr)
	}

	return page
}

// newKeysCursorNode is the constructor shared by the pagination tests.
// Replication=1 keeps the test focused on the per-node enumeration
// path without ring-routing to peers.
func newKeysCursorNode(t *testing.T) *backend.DistMemory {
	t.Helper()

	addr := AllocatePort(t)

	bi, err := backend.NewDistMemory(
		context.Background(),
		backend.WithDistNode("keys-A", addr),
		backend.WithDistReplication(1),
	)
	if err != nil {
		t.Fatalf("new dist memory: %v", err)
	}

	dm, ok := bi.(*backend.DistMemory)
	if !ok {
		t.Fatalf("expected *backend.DistMemory, got %T", bi)
	}

	StopOnCleanup(t, dm)

	return dm
}

// TestDistKeysCursor_WalksAllShards is the core C.2 contract: a
// client following the next_cursor chain from /internal/keys must
// eventually visit every key the node holds, exactly once.
//
// We seed 64 keys (well under any reasonable per-shard limit so no
// page is truncated) then walk the cursor chain until next_cursor
// is empty, accumulating keys. The set of accumulated keys must
// equal the set of seeded keys.
func TestDistKeysCursor_WalksAllShards(t *testing.T) {
	t.Parallel()

	dm := newKeysCursorNode(t)
	ctx := context.Background()

	const want = 64

	expected := make(map[string]struct{}, want)
	for i := range want {
		key := "cursor-key-" + strconv.Itoa(i)

		err := dm.Set(ctx, &cache.Item{
			Key:         key,
			Value:       []byte("v"),
			Version:     1,
			Origin:      "keys-A",
			LastUpdated: time.Now(),
		})
		if err != nil {
			t.Fatalf("set %s: %v", key, err)
		}

		expected[key] = struct{}{}
	}

	base := "http://" + dm.LocalNodeAddr()

	got := map[string]int{} // key -> times seen
	cursor := ""

	for range 32 { // upper bound to prevent infinite loop on bug
		page := fetchKeysPage(ctx, t, base, cursor, 0)

		for _, k := range page.Keys {
			got[k]++
		}

		if page.NextCursor == "" {
			break
		}

		cursor = page.NextCursor
	}

	if len(got) != len(expected) {
		t.Fatalf("expected %d unique keys across pagination, got %d", len(expected), len(got))
	}

	for k := range expected {
		if got[k] != 1 {
			t.Fatalf("key %s appeared %d times across pagination, want 1", k, got[k])
		}
	}
}

// TestDistKeysCursor_LimitTruncatesAndResumes proves the per-shard
// `limit` knob works: when a shard has more keys than the limit
// allows, the response carries `truncated=true` and `next_cursor`
// pointing at the same shard so the client can re-request with a
// larger limit. We assert at least one truncated page surfaces when
// many keys land in a single shard.
//
// The default shard count (8) means 256 keys distribute roughly to
// 32 per shard; setting limit=1 guarantees truncation on every
// non-empty shard.
func TestDistKeysCursor_LimitTruncatesAndResumes(t *testing.T) {
	t.Parallel()

	dm := newKeysCursorNode(t)
	ctx := context.Background()

	for i := range 256 {
		key := "limit-key-" + strconv.Itoa(i)

		err := dm.Set(ctx, &cache.Item{
			Key:         key,
			Value:       []byte("v"),
			Version:     1,
			Origin:      "keys-A",
			LastUpdated: time.Now(),
		})
		if err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}

	base := "http://" + dm.LocalNodeAddr()
	page := fetchKeysPage(ctx, t, base, "0", 1)

	if !page.Truncated {
		t.Fatalf("expected truncated=true with limit=1 across 256 keys, got false")
	}

	if page.NextCursor != "0" {
		t.Fatalf("expected truncated page to keep next_cursor=0, got %q", page.NextCursor)
	}

	if len(page.Keys) != 1 {
		t.Fatalf("expected exactly 1 key under limit=1, got %d", len(page.Keys))
	}
}
