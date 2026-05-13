package tests

import (
	"context"
	"strconv"
	"testing"

	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// seedListKeysCluster writes `count` items via their primary owner
// so each key lands with full replication. Returns the cluster the
// caller can issue ListKeys against. Mirrors the seeding shape of
// TestDistMemoryForwardingReplication.
func seedListKeysCluster(t *testing.T, count int) *DistCluster {
	t.Helper()

	dc := SetupInProcessCluster(t, 5)

	for i := range count {
		key := "first-" + strconv.Itoa(i+1)

		owners := dc.Ring.Lookup(key)
		if len(owners) == 0 {
			t.Fatalf("no owners for key %s", key)
		}

		item := &cache.Item{Key: key, Value: key}

		err := item.Valid()
		if err != nil {
			t.Fatalf("item valid %s: %v", key, err)
		}

		primary := dc.ByID(owners[0])
		if primary == nil {
			t.Fatalf("unexpected owner id %s for %s", owners[0], key)
		}

		err = primary.Set(context.Background(), item)
		if err != nil {
			t.Fatalf("set %s via %s: %v", key, owners[0], err)
		}
	}

	return dc
}

// TestDistListKeys_ClusterWideDedup writes 50 keys to a 5-node
// cluster with RF=3 and asserts ListKeys returns each key exactly
// once — proving the cluster-wide dedup across replicas works.
func TestDistListKeys_ClusterWideDedup(t *testing.T) {
	t.Parallel()

	const count = 50

	dc := seedListKeysCluster(t, count)

	res, err := dc.Nodes[0].ListKeys(context.Background(), "", 0)
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}

	if len(res.Keys) != count {
		t.Fatalf("expected %d unique keys, got %d (truncated=%v)", count, len(res.Keys), res.Truncated)
	}

	if res.Truncated {
		t.Fatalf("unexpected truncation: %+v", res)
	}

	if len(res.PartialNodes) != 0 {
		t.Fatalf("expected zero partial nodes, got %v", res.PartialNodes)
	}

	seen := make(map[string]int, len(res.Keys))
	for _, k := range res.Keys {
		seen[k]++
	}

	for k, n := range seen {
		if n != 1 {
			t.Fatalf("key %s appears %d times — dedup failed", k, n)
		}
	}
}

// TestDistListKeys_PrefixFilter pins the prefix-mode classifier:
// a non-glob pattern is matched via strings.HasPrefix, so seeding
// keys with two distinct prefixes yields a clean split.
func TestDistListKeys_PrefixFilter(t *testing.T) {
	t.Parallel()

	dc := SetupInProcessCluster(t, 5)

	put := func(key string) {
		owners := dc.Ring.Lookup(key)
		item := &cache.Item{Key: key, Value: key}

		err := item.Valid()
		if err != nil {
			t.Fatalf("item valid %s: %v", key, err)
		}

		err = dc.ByID(owners[0]).Set(context.Background(), item)
		if err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}

	for i := range 10 {
		put("first-" + strconv.Itoa(i))
	}

	for i := range 5 {
		put("second-" + strconv.Itoa(i))
	}

	res, err := dc.Nodes[2].ListKeys(context.Background(), "first-", 0)
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}

	if len(res.Keys) != 10 {
		t.Fatalf("expected 10 first-* keys, got %d: %v", len(res.Keys), res.Keys)
	}

	for _, k := range res.Keys {
		if k[:6] != "first-" {
			t.Fatalf("non-first prefix in result: %s", k)
		}
	}
}

// TestDistListKeys_GlobFilter pins the glob branch: `first-?` is a
// single-character wildcard, so it should hit `first-1`..`first-9`
// (nine entries) and miss `first-10`.
func TestDistListKeys_GlobFilter(t *testing.T) {
	t.Parallel()

	dc := seedListKeysCluster(t, 15)

	res, err := dc.Nodes[3].ListKeys(context.Background(), "first-?", 0)
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}

	if len(res.Keys) != 9 {
		t.Fatalf("expected 9 single-digit first-? keys, got %d: %v", len(res.Keys), res.Keys)
	}
}

// TestDistListKeys_MaxTruncates pins that the `max` cap surfaces
// via Truncated=true and bounds the result set size.
func TestDistListKeys_MaxTruncates(t *testing.T) {
	t.Parallel()

	dc := seedListKeysCluster(t, 50)

	res, err := dc.Nodes[0].ListKeys(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}

	if !res.Truncated {
		t.Fatalf("expected truncated=true at max=10 against 50 keys, got %+v", res)
	}

	if len(res.Keys) != 10 {
		t.Fatalf("expected exactly 10 keys at max=10, got %d", len(res.Keys))
	}
}

// TestDistListKeys_InvalidPattern surfaces malformed globs as a
// caller-visible error rather than silently returning an empty
// result.
func TestDistListKeys_InvalidPattern(t *testing.T) {
	t.Parallel()

	dc := SetupInProcessCluster(t, 2)

	_, err := dc.Nodes[0].ListKeys(context.Background(), "[unclosed", 0)
	if err == nil {
		t.Fatalf("expected error for malformed glob, got nil")
	}
}
