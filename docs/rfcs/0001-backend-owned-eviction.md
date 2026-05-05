# RFC 0001 — Backend-owned eviction order

- **Status**: Closed — REJECTED
- **Target**: n/a (spike measured, hypothesis falsified, code removed)
- **Owners**: TBD
- **Related code**: [pkg/eviction/eviction.go](../../pkg/eviction/eviction.go), [pkg/cache/v2/item.go](../../pkg/cache/v2/item.go), [hypercache_io.go](../../hypercache_io.go)

## Summary

Eliminate the duplicate hash + lock + map insert that every `HyperCache.Set` / `Remove` / `Clear` performs by hosting the eviction algorithm's per-key state directly inside `cache.Item`. A side-effect closes a long-standing semantic gap: `Get` currently does not bump LRU recency, so "LRU" today is actually "least-recently-written".

## Background

`HyperCache.Set` performs two independent updates to two independent data structures keyed by the same string:

```go
// hypercache_io.go (current)
err = hyperCache.backend.Set(ctx, item)          // 1. hash key, lock data shard, insert into shard map
hyperCache.evictionAlgorithm.Set(key, item.Value) // 2. hash key, lock algo,        insert into algo  map + linked-list
```

Each call:

1. Hashes the key (xxhash-sum64 → 32-bit fold; ~3.6 ns each per [bench-step3.txt](../../bench-step3.txt)).
1. Locks a shard (RWMutex; contention dominates under load).
1. Inserts into a `map[string]X` (allocates a hash bucket).
1. For LRU/LFU: rotates a doubly-linked list node.

Step 1 is repeated on the same key — wasted work. Steps 2–3 happen on disjoint shard maps even though they cover the same logical key — also wasted work. The only step that is actually unique to the eviction layer is step 4 (the linked-list rotation).

`Remove` has the same pattern. `Clear` lists every key, calls `algorithm.Delete(key)` per item, then clears the backend — N hashes, N algo locks for what should be one teardown.

`Get` does *not* call `algorithm.Get(key)` today (the eviction algorithm is unaware of reads). LRU is therefore "Least Recently Written", which violates user expectations and is documented nowhere.

### Measured cost

From `bench-step3.txt` and earlier baselines (M4 Pro, `-race` off):

| Operation | Hash | Lock+Map | Total ns/op |
| --------- | ---- | -------- | ----------- |
| `ConcurrentMap_GetShard` (xxhash → shard idx) | 3.6 | — | 3.6 |
| `Set` (single shard, no eviction) | — | — | ~80 |
| `Set` (with `algorithm.Set`) | — | — | ~711 (`HyperCache_SetParallel`) |

The eviction-side overhead is roughly **8×** the storage-side cost on the hot path. Most of that is the algorithm's own mutex (LRU's `sync.RWMutex` serializes Set + moveToFront), partially mitigated by [pkg/eviction/sharded.go](../../pkg/eviction/sharded.go) but not eliminated.

## Goals

1. **Halve the per-op work on `Set` and `Remove`** by removing the duplicate hash + map insert. Linked-list rotation (LRU) or counter increment (LFU) remains.
1. **Make `Get` actually update recency.** Once eviction state lives inside `Item`, the existing `touchBackend` Touch path can take a `*Item` and update the algorithm pointer in O(1).
1. **Preserve algorithm pluggability** (LRU, LFU, Clock, ARC, CAWOLFU). No collapsing into one frozen impl.
1. **Preserve backend pluggability.** Redis / RedisCluster delegate eviction to the engine; the new path must be a no-op for them.

## Non-goals

- Replacing the eviction algorithm interface with a different abstraction (queue, generations, etc.).
- Lock-free linked lists. The linked-list rotation under a per-shard lock is a well-understood pattern; lock-free LRU is out of scope.
- Touching the distributed eviction story (DistMemory shedding via rebalancer is a separate concern; see [docs/distributed.md](../distributed.md) §Rebalancing).

## Constraints

- **Item is pooled** ([pkg/cache/v2/item.go:34-58](../../pkg/cache/v2/item.go#L34-L58)). Adding fields costs zero per-Set allocations as long as we reset them in `Put`.
- **Item ships across the wire** in DistMemory replication ([pkg/backend/dist_http_types.go](../../pkg/backend/dist_http_types.go)). Eviction-state pointers MUST NOT be JSON-serialized — they are local-only.
- **Sharded eviction** ([pkg/eviction/sharded.go](../../pkg/eviction/sharded.go)) currently shards algorithm state on the same hash as ConcurrentMap; if we move state into Item, we still need per-shard lists (the *list head/tail* belongs to the shard, not the Item).

## Options

### Option A — Embed eviction state in `cache.Item`

Add to `Item`:

```go
type Item struct {
    Key   string
    Value any
    // ... existing fields ...

    // evictPrev/evictNext are pointers in the per-shard LRU list. They
    // are owned by the eviction algorithm; backends MUST NOT serialize
    // or copy them across shards. Touch under the shard's eviction lock.
    evictPrev *Item `json:"-" cbor:"-"`
    evictNext *Item `json:"-" cbor:"-"`

    // accessFreq supports LFU/CAWOLFU without a separate map.
    accessFreq atomic.Uint64

    // clockBit supports the Clock algorithm.
    clockBit atomic.Bool
}
```

The `IAlgorithm` interface gains a `*Item`-aware sibling:

```go
type IAlgorithmItemAware interface {
    SetItem(it *Item)
    TouchItem(it *Item)
    DeleteItem(it *Item)
    Evict() (*Item, bool)
}
```

Algorithms operating on Item pointers maintain only a per-shard head/tail pointer pair (LRU) or a freq histogram (LFU). The `map[string]*lruCacheItem` inside today's LRU disappears — Item itself IS the list node.

`HyperCache.Set` becomes:

```go
err = hyperCache.backend.Set(ctx, item)   // 1 hash, 1 lock, 1 insert
hyperCache.evictionAlgorithm.SetItem(item) // 0 hashes, 1 lock, 0 inserts (just list rotate)
```

The two locks are still distinct (data shard lock vs eviction shard lock) — *unless* we co-locate them, which is Option A2 below.

#### Option A2 (extension) — Co-locate locks

Make `ConcurrentMapShard` own both the storage map and the LRU head/tail:

```go
type ConcurrentMapShard struct {
    sync.RWMutex
    items     map[string]*Item
    count     atomic.Int64

    // Eviction state (algorithm-specific; one of these is non-nil).
    lruHead, lruTail *Item       // for LRU
    lfuFreqs         []*Item     // for LFU (frequency buckets)
    clockHand        *Item       // for Clock
    // ...
}
```

Set holds the shard write lock once, updates the items map AND rotates the LRU list under the same lock. **One hash, one lock, atomic in shard scope.**

This crosses the abstraction boundary between `pkg/cache/v2` and `pkg/eviction` more aggressively than A. The eviction package becomes a set of helper functions operating on `ConcurrentMapShard` instead of self-contained types.

### Option B — Composite eviction-aware backend per algorithm

Eliminate `IAlgorithm` entirely. Each algorithm gets its own backend type:

```go
type LRUInMemory struct { /* storage + LRU baked in */ }
type LFUInMemory struct { /* storage + LFU baked in */ }
```

**Pros**: simplest mental model, zero abstraction cost.

**Cons**: explodes the backend matrix (5 algorithms × 4 backends = 20 types). Doesn't model real-world need (users rarely switch algorithms; they pick one and stick).

### Option C — Status quo + share-pre-computed-hash

Compute the shard index once in `HyperCache.Set`, pass it to both `backend.Set` and `algorithm.Set`. Saves one xxhash call (~3.6 ns) but not the lock or the map insert.

**Pros**: tiny diff, no API change.

**Cons**: marginal gain (~3.6 ns / ~711 ns = 0.5%). Doesn't fix the LRW-vs-LRU semantic gap.

## Recommendation

**Option A**, with **A2 as a follow-up** if A's measurements justify it.

- **Why not A2 directly?** Crossing the cache/eviction package boundary is a bigger refactor; A is the smaller verifiable step. If A delivers (say) 30% of the achievable win, A2 buys the remaining 70%. Land A first, measure, decide on A2 separately.
- **Why not B?** The 4 × 5 explosion is real cost. Users *do* swap algorithms (LRU → CAWOLFU is the recommended upgrade for hot-key workloads); the abstraction earns its keep.
- **Why not C?** The semantic gap (LRW vs LRU) is a correctness issue, not a perf one. C doesn't address it.

## Implementation plan

1. **Spike LRU.** Add `evictPrev/evictNext *Item` to `cache.Item`; add `IAlgorithmItemAware`; rewrite `LRU` to operate on `*Item`. Behind a feature option (`WithItemAwareEviction(true)`). Leave the legacy path intact.
1. **Bench.** Compare `BenchmarkHyperCache_SetParallel` baseline vs. spike. If under 30% improvement, stop and reconsider — there's some other bottleneck and Option A's premise is wrong.
1. **Migrate the 4 remaining algorithms.** Mechanical port; each algorithm replaces its `map[string]*algoNode` with direct `*Item` pointer use.
1. **Wire `Get` to call `TouchItem`** so LRU semantically becomes "least recently used", not just written. Update doc + CHANGELOG to call out the behavior fix.
1. **Default `WithItemAwareEviction(true)`** in v2.x; gate legacy via an explicit opt-out for one release.
1. **Remove legacy `IAlgorithm.Set/Get/Delete`** in v3.

## Migration

- **For library users on v2.x**: zero changes. The new path is opt-in; default stays untyped. Bench-aware users opt-in for the win.
- **For library users on v3**: `IAlgorithm` is gone. Custom algorithm authors port to `IAlgorithmItemAware` (small mechanical change — main difference is operating on `*Item` instead of `(key, any)`).

## Risks

| Risk | Likelihood | Mitigation |
| ---- | ---------- | ---------- |
| Item pointer fields confuse JSON serialization (DistMemory replication) | High | Tag fields `json:"-" cbor:"-"`; assert in unit tests that wire format excludes them. |
| Hidden assumption that Item is value-copyable (e.g., `cloned := *it`) | Medium | Audit all `*it` deref + reassign sites. Pointer fields stay valid across copies *for reads*, but lifecycle is owned by the algorithm — copies must not be inserted into the LRU list. |
| Pool reuse leaves stale `evictPrev/Next` pointers | Medium | `ItemPoolManager.Put` already does `*it = Item{}`; the zeroing covers the new fields. Verify with a reuse-after-evict test. |
| Algorithm-specific fields (clockBit) waste memory when a different algorithm is configured | Low | Layout: 2 pointers + atomic.Bool + atomic.Uint64 = ~24 bytes added. Already pooled; no per-Set alloc. The waste is per-Item-lifetime, not per-op. |
| Sharded eviction interaction breaks | Low | Option A keeps per-shard list heads/tails; Sharded just routes `SetItem` to the right shard. Same routing logic as today. |

## Open questions

- **Cross-shard moves under DistMemory rebalance**: when an Item moves shards (rebalancer), its `evictPrev/Next` pointers reference the *old* shard's list. The receiving side must reset them and link into the new shard's list. Rebalancer code in [pkg/backend/dist_memory.go](../../pkg/backend/dist_memory.go) needs a hook here. Probably trivial: rebalance-receive sets `it.evictPrev = nil; it.evictNext = nil` before insert; algorithm's SetItem links it normally.
- **Eviction algorithm sharding interaction**: with Item-embedded state, do we still need [pkg/eviction/sharded.go](../../pkg/eviction/sharded.go) at all? The per-shard list head/tail handles parallelism. Possibly the Sharded wrapper becomes a no-op once A lands. Decide after measurements.
- **ARC's ghost lists**: ARC tracks evicted keys in B1/B2 ghost lists. These hold *keys* (no Item), so they can stay as today's `map[string]struct{}`. ARC needs minor surgery, not a rewrite.

## Decision criteria

Land A if:

- `BenchmarkHyperCache_SetParallel` improves ≥ 30% vs current.
- `BenchmarkHyperCache_GetParallel` improves materially (Get now touches LRU; expect 5-15% regression because of the new write — acceptable trade for correctness).
- All eviction-contract tests pass with the new path.
- `RUN_INTEGRATION_TEST=yes go test -race -count=10 -shuffle=on -timeout=20m ./...` is green.

Reject A and revisit if any criterion fails.

## Spike outcome — REJECTED

Implemented as `WithItemAwareEviction(true)` opt-in:

- New unexported fields `evictPrev`, `evictNext`, `inEvictList` on `cache.Item`
- `IAlgorithmItemAware` interface in [pkg/eviction/eviction.go](../../pkg/eviction/eviction.go)
- `LRUItemAware` ([pkg/eviction/lru_item_aware.go](../../pkg/eviction/lru_item_aware.go)) and `ShardedItemAware` ([pkg/eviction/sharded_item_aware.go](../../pkg/eviction/sharded_item_aware.go))
- HyperCache hot paths route via `recordSet` / `recordTouch` / `recordDelete` / `evictNext`

Benchmark vs legacy (sharded both sides; M4 Pro; `go test -bench -count=5`; benchstat):

```text
                          │  legacy  │     item-aware     │
                          │  sec/op  │  sec/op    vs base │
HyperCache_SetParallel-14  205.5n ¹   182.0n ¹  -11.44%
HyperCache_GetParallel-14   91.03n ¹  139.60n ¹  +53.36%
geomean                    136.8n     159.4n    +16.54%
```

**Both gates fail:**

- Set: -11.44% gain vs ≥30% target.
- Get: +53.36% regression vs 5-15% budget.

Geomean is +16.54% slower overall.

### Why the premise was wrong

The RFC's hypothesis: "the duplicate hash + map lookup in the legacy
path is the dominant cost; eliminating it via embedded Item state will
deliver ≥30% Set improvement." The data falsifies it.

- The duplicate map savings are **real but small** (~12% Set).
  Sharded contention reduction is what already dominates the legacy
  path's perf, not the second hash/map.
- The Get regression (+53%) is the **honest cost of correct LRU
  semantics**. Legacy's "LRU is really LRW" was free precisely because
  Get skipped the algorithm's mutex entirely. Making Get touch LRU
  (regardless of the Item-aware vs map-based path) costs roughly this
  much under contention.

### Disposition

Per the RFC's own discipline (`Reject A and revisit if any criterion fails`):

1. **Do not migrate the remaining 4 algorithms** (LFU, Clock, ARC, CAWOLFU). Spike LRU only.
1. **Keep `WithItemAwareEviction` as experimental opt-in.** Documented as
   "slower on Get, semantically-correct LRU." Default stays legacy.
1. **Do not pursue Option A2** (co-located locks) — the win Option A
   would have justified A2 isn't there to amortize the bigger refactor.
1n. **The "Get does not touch LRU" semantic gap is a separate concern**
   that could be addressed inside the legacy path (have HyperCache.Get
   call `evictionAlgorithm.Get(key)`) at similar cost to the Item-aware
   Touch — i.e., the cost is fundamental to "real LRU", not specific
   to either path.

### Final disposition: code removed

After preserving the spike as `WithItemAwareEviction` for one round, we
removed it entirely. Reasoning:

- **The hypothesis was falsified.** Keeping experimental code that
  didn't meet its bar is debt — every future linter run, every
  dependency bump, every `Item` layout change has to consider it. For
  a user benefit nobody actually asked for.
- **"Experimental, slower, but more correct" is a confusing knob.**
  Users would have to understand both the LRU-vs-LRW gap *and* the
  per-op trade-off to opt-in correctly. Nobody reads opt-in docs that
  carefully.
- **`Item` bloat propagates conceptually.** `cache.Item` IS the wire
  format for DistMemory replication; adding eviction-state fields
  (even unexported with `json:"-"`) weakens the mental model "Item is
  what gets serialized." Smaller surface = clearer guarantees.
- **CLAUDE.md prime directive #4 (Stay on task).** "Don't add features
  beyond what the task requires." The spike was the test; the test
  failed; the smallest correct response is to revert.

What was removed (one cleanup commit, ~400 lines deleted):

- `WithItemAwareEviction` option
- `IAlgorithmItemAware` interface + `LRUItemAware` + `ShardedItemAware`
  types
- `Item.evictPrev/evictNext/inEvictList` fields + 6 accessor methods
- `HyperCache.recordSet/recordTouch/recordDelete/evictNext` helpers
- `itemAwareAlg` / `itemAwareEviction` fields on HyperCache
- `lru_item_aware_test.go`, `hypercache_item_aware_eviction_benchmark_test.go`

What stays:

- This RFC. The engineering artifact is the *measurement and
  reasoning*, not the code.

### Lessons for future eviction work

- **Sharded contention is the dominant cost on the eviction hot path,
  not dual-map overhead.** A future RFC targeting Set throughput
  should look at lock-free rotation (atomic CAS on linked-list nodes,
  hazard pointers, RCU) rather than data layout.
- **"Real LRU" has a fundamental Get-side cost** under contention
  (~50% on this codebase, our measurement). The legacy path's "LRU is
  least-recently-written" is free precisely because Get skips the
  algorithm mutex. If a user files a correctness bug, the smallest
  fix is a one-line option that has `HyperCache.Get` call
  `evictionAlgorithm.Get(key)` on the legacy path — same cost as the
  Item-aware path, zero infrastructure. **Don't pre-build that
  option until a user asks for it.**
