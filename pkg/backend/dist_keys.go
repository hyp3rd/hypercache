package backend

import (
	"context"
	"log/slog"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/hyp3rd/ewrap"
	"golang.org/x/sync/errgroup"

	"github.com/hyp3rd/hypercache/internal/cluster"
)

// keyGlobMetacharacters is the set of bytes that, when present in a
// pattern, switch the matcher from prefix-mode to glob-mode. Chosen
// to match path.Match's syntax: '*' (any sequence), '?' (single
// character), '[' (character class).
const keyGlobMetacharacters = "*?["

// localKeysInitialCap is the starting capacity for the per-node
// matching-keys slice. Picked to cover the common operator-debug
// shape (filter hits a few dozen keys per shard) while avoiding a
// large up-front allocation when the matcher rejects most of the
// shard.
const localKeysInitialCap = 64

// defaultListKeysMax is the fallback cap applied when neither the
// caller nor the WithDistListKeysMax option specifies one. Picked
// to comfortably cover the typical operator-debug "list everything
// matching this prefix" workload (5-node × tens of thousands of
// keys per node) while still bounding worst-case memory.
const defaultListKeysMax = 10000

// buildKeyMatcher returns a predicate that decides whether a given
// key matches `pattern`. Three modes:
//
//   - empty pattern: matches every key (no filter).
//   - pattern contains glob metacharacters: full glob match via
//     path.Match. Validation is done up-front by feeding a probe
//     through path.Match; an invalid pattern (e.g. unmatched `[`)
//     surfaces as ErrBadPattern to the caller instead of every
//     subsequent match failing silently.
//   - otherwise: treated as a literal prefix via strings.HasPrefix.
//
// Glob syntax is matched with `path.Match`, not `filepath.Match`:
// arbitrary string keys don't have OS-specific path-separator
// semantics, so the platform-agnostic version is what we want.
func buildKeyMatcher(pattern string) (func(string) bool, error) {
	if pattern == "" {
		return func(string) bool { return true }, nil
	}

	if !strings.ContainsAny(pattern, keyGlobMetacharacters) {
		return func(k string) bool { return strings.HasPrefix(k, pattern) }, nil
	}

	// Validate the glob up-front. path.Match returns
	// path.ErrBadPattern for malformed inputs and a value-dependent
	// error otherwise — running it against a fixed sentinel surfaces
	// the structural error without taking a position on whether
	// real keys would actually match.
	_, err := path.Match(pattern, "")
	if err != nil {
		return nil, ewrap.Wrap(err, "list-keys: invalid glob pattern")
	}

	return func(k string) bool {
		ok, mErr := path.Match(pattern, k)
		// Pattern was already validated above; runtime mismatch
		// errors here would be impossible. Treat as no-match to
		// stay best-effort.
		return mErr == nil && ok
	}, nil
}

// ListKeysResult bundles a cluster-wide key enumeration with the
// best-effort accounting the caller needs to communicate partial
// results to the operator. `Keys` is sorted and deduplicated across
// owners. `Truncated` is true when the merged set hit `max` and we
// stopped pulling further pages. `PartialNodes` lists peers whose
// fan-out call failed — their keys may be missing from `Keys`.
type ListKeysResult struct {
	Keys         []string
	Truncated    bool
	PartialNodes []string
}

// listKeysAccumulator is the cross-goroutine merge state for a
// single ListKeys fan-out call. Held by reference so each peer
// goroutine can lock the mutex and contribute its slice without
// closing over a stack-local map.
type listKeysAccumulator struct {
	mu        sync.Mutex
	dedup     map[string]struct{}
	maxKeys   int
	partial   []string
	truncated bool
}

// tryAdd merges `keys` into the dedup set under the lock. Stops at
// maxKeys and flips truncated. Idempotent — a key already in the
// set doesn't change accounting.
func (a *listKeysAccumulator) tryAdd(keys []string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for _, k := range keys {
		if len(a.dedup) >= a.maxKeys {
			a.truncated = true

			return
		}

		a.dedup[k] = struct{}{}
	}
}

// markPartial records a peer whose fan-out call failed. The peer's
// keys may be missing from the merged result.
func (a *listKeysAccumulator) markPartial(peer cluster.NodeID) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.partial = append(a.partial, string(peer))
}

// result projects the accumulator into the public ListKeysResult.
// Sorts deterministically so paging across requests is stable.
func (a *listKeysAccumulator) result() ListKeysResult {
	keys := make([]string, 0, len(a.dedup))
	for k := range a.dedup {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	return ListKeysResult{
		Keys:         keys,
		Truncated:    a.truncated,
		PartialNodes: a.partial,
	}
}

// ListKeys enumerates keys across every alive peer (including this
// node), deduplicates by string identity, sorts, and returns up to
// `max` results. `pattern` follows the same prefix/glob rules as
// `buildKeyMatcher`. A `max` of 0 falls back to defaultListKeysMax;
// the hard ceiling lives in the v1 handler that calls this method.
//
// Per-peer failures (transport error, unreachable, etc.) don't
// fail the whole call — best-effort matches the read-repair and
// hint-replay contracts elsewhere in the cluster. Failed peers are
// returned in PartialNodes so the caller can surface a banner to
// the operator.
func (dm *DistMemory) ListKeys(ctx context.Context, pattern string, maxResults int) (ListKeysResult, error) {
	if maxResults <= 0 {
		maxResults = defaultListKeysMax
	}

	matcher, err := buildKeyMatcher(pattern)
	if err != nil {
		return ListKeysResult{}, err
	}

	acc := &listKeysAccumulator{
		dedup:   make(map[string]struct{}, maxResults),
		maxKeys: maxResults,
	}

	peers := dm.alivePeerIDs()
	transport := dm.loadTransport()

	g, gctx := errgroup.WithContext(ctx)

	for _, peer := range peers {
		g.Go(func() error {
			dm.collectPeerKeys(gctx, peer, pattern, matcher, transport, acc)

			return nil
		})
	}

	_ = g.Wait() // every goroutine swallows its error; Wait can't fail.

	return acc.result(), nil
}

// collectPeerKeys fetches one peer's matching keys and merges them
// into `acc`. Three branches:
//   - self: walk local shards directly (no transport hop).
//   - transport torn down: mark partial.
//   - peer hop: best-effort fetch; failure → mark partial + log.
func (dm *DistMemory) collectPeerKeys(
	ctx context.Context,
	peer cluster.NodeID,
	pattern string,
	matcher func(string) bool,
	transport DistTransport,
	acc *listKeysAccumulator,
) {
	if peer == dm.localNode.ID {
		acc.tryAdd(dm.localMatchingKeys(matcher))

		return
	}

	if transport == nil { // cluster torn down mid-call
		acc.markPartial(peer)

		return
	}

	keys, err := transport.ListKeys(ctx, string(peer), pattern)
	if err != nil {
		if dm.logger != nil {
			dm.logger.Debug(
				"list-keys: peer fan-out failed",
				slog.String("peer", string(peer)),
				slog.Any("err", err),
			)
		}

		acc.markPartial(peer)

		return
	}

	acc.tryAdd(keys)
}

// alivePeerIDs returns the membership snapshot's alive nodes
// (including self). Suspect/Dead nodes are excluded — their key
// sets aren't fresh enough to be worth the per-request HTTP cost.
// Falls back to a self-only list when membership isn't wired up
// (single-process / test scenarios).
func (dm *DistMemory) alivePeerIDs() []cluster.NodeID {
	if dm.membership == nil {
		if dm.localNode != nil {
			return []cluster.NodeID{dm.localNode.ID}
		}

		return nil
	}

	nodes := dm.membership.List()
	out := make([]cluster.NodeID, 0, len(nodes))

	for _, n := range nodes {
		if n == nil || n.State != cluster.NodeAlive {
			continue
		}

		out = append(out, n.ID)
	}

	return out
}

// localMatchingKeys walks the local shards once, returning keys that
// satisfy the matcher. Used by ListKeys for the self-peer slice so
// we don't pay an HTTP roundtrip to talk to ourselves.
func (dm *DistMemory) localMatchingKeys(matcher func(string) bool) []string {
	out := make([]string, 0, localKeysInitialCap)

	for _, sh := range dm.shards {
		if sh == nil {
			continue
		}

		for k := range sh.items.All() {
			if matcher(k) {
				out = append(out, k)
			}
		}
	}

	return out
}
