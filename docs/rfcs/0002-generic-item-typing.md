# RFC 0002 — `Item[V any]` generic value typing

- **Status**: Draft
- **Target**: v2.x (wrapper, additive) → v3.0 (deep generics, breaking)
- **Owners**: TBD
- **Related code**: [pkg/cache/v2/item.go](../../pkg/cache/v2/item.go), [pkg/cache/v2/cmap.go](../../pkg/cache/v2/cmap.go), [hypercache_io.go](../../hypercache_io.go)

## Summary

Eliminate the `any`-based ergonomics of `HyperCache.Get` (every call site type-asserts) by offering a typed wrapper API in v2.x and a fully-generic `Item[V]` chain in v3. The two-phase approach gives users the typed surface they want now without forcing the deeper refactor immediately.

## Background

`Item.Value` is `any`:

```go
// pkg/cache/v2/item.go:62-72
type Item struct {
    Key   string
    Value any
    // ...
}
```

User code on every Get:

```go
val, ok := cache.Get(ctx, "session:42")
if !ok {
    return nil
}
session, ok := val.(*Session) // unsafe in general; tedious
```

Wrong type assertions panic at runtime. There is no compile-time guarantee that what was Set as `*Session` is what Get returns. In a high-stakes financial environment this is a class of latent bug the type system *can* prevent — generics are the right tool.

`HyperCache` already takes one type parameter `T backend.IBackendConstrain` to bind the cache to a specific backend impl. Adding a second value-type parameter is the natural next step.

### What's actually generic-able

The whole stack stores `Item.Value`:

- `pkg/cache/v2.ConcurrentMap` — `map[string]*Item`
- `pkg/eviction.IAlgorithm` — `Set(key string, value any)`, `Get(key string) (any, bool)`
- `pkg/backend.IBackend[T]` — `Set(ctx, *Item)`, `Get(ctx, key) (*Item, bool)`
- `HyperCache[T]` — public CRUD methods

Eviction algorithms don't *inspect* the value (LRU/LFU/Clock track recency/frequency, not the bytes); they could stay `any`. ConcurrentMap, IBackend, and HyperCache all touch the value semantically.

### What complicates this

- **Heterogeneous values are real.** Some users cache (URL, []byte) and (sessionID, *Session) in the same instance. Forcing a single V regresses that workflow.
- **Backends serialize values** (Redis, RedisCluster). The serializer accepts `any` today via reflection. With concrete V, we either need V-aware serializers or keep the boundary at `any`.
- **DistMemory replicates Items across the wire** as JSON. Generic V vs `any` is invisible at the JSON layer; serialization stays string-keyed.
- **The pool** (`ItemPoolManager`) holds `*Item`. Generic Item[V] pools are per-V — type-erased pools don't work without reflection.

## Goals

1. **Compile-time type safety** for Get/Set on the common single-V case.
1. **Zero forced migration** for v2.x users — typed surface is opt-in.
1. **No double type assertion**: a typed-wrapper `Get` returns `V`, not `(any, type-assert-then-V)`.
1. **No runtime cost beyond a single type assertion** (in v2.x wrapper) → zero in v3 (full generics).

## Non-goals

- Removing the untyped `HyperCache` API. It stays for heterogeneous use cases.
- Generics on the eviction algorithm layer. Algorithms don't read V; staying `any` saves a parameter.
- Generics on the backend tag `T`. Already typed; no change.

## Options

### Option A — `hypercache.Typed[V]` wrapper (v2.x; additive)

A thin wrapper holding an existing `*HyperCache[T]` plus a phantom V:

```go
// hypercache_typed.go (new)
package hypercache

type Typed[T backend.IBackendConstrain, V any] struct {
    hc *HyperCache[T]
}

func NewTyped[T backend.IBackendConstrain, V any](hc *HyperCache[T]) *Typed[T, V] {
    return &Typed[T, V]{hc: hc}
}

func (t *Typed[T, V]) Set(ctx context.Context, key string, value V, expiration time.Duration) error {
    return t.hc.Set(ctx, key, value, expiration)
}

func (t *Typed[T, V]) Get(ctx context.Context, key string) (V, bool) {
    var zero V

    raw, ok := t.hc.Get(ctx, key)
    if !ok {
        return zero, false
    }

    v, ok := raw.(V)
    if !ok {
        // Cache held the wrong type for this key — treat as miss to
        // avoid surfacing a runtime panic to the caller. Document this
        // explicitly: typed wrappers SHOULD be the only writer for a
        // given key, and cross-typed reads are caller error.
        return zero, false
    }

    return v, true
}

// Same wrapping for GetOrSet, GetMultiple, GetWithInfo, Remove, etc.
```

Construction:

```go
hc, _ := hypercache.NewInMemoryWithDefaults(ctx, 10_000)
sessions := hypercache.NewTyped[backend.InMemory, *Session](hc)

sessions.Set(ctx, "u:42", &Session{...}, time.Hour)
s, ok := sessions.Get(ctx, "u:42") // s is *Session
```

**Pros**:

- **Zero breaking changes.** Users who don't want types keep what they have.
- **Tiny diff.** New file, ~150 lines, no touches to existing code paths.
- **Composable.** A single underlying `HyperCache[T]` can have multiple `Typed[T, V]` views over disjoint keyspaces (`sessions`, `tokens`, etc.).
- **Sets the v3 expectation.** Once we ship the typed surface, users who want it migrate; the demand signal informs whether v3's deep generics are worth the cost.

**Cons**:

- **Still `any` underneath.** Each Get does one type-assert at the wrapper layer. Cost: a single type-check (~1-2 ns).
- **Wrong-type-stored returns miss-not-error.** Documented as caller-side discipline ("don't share a key across Typed instances of different V"); not enforced.
- **Doesn't simplify `Item.Value`** — internal callers still see `any`.

### Option B — Deep generics: `Item[V]`, `ConcurrentMap[V]`, `HyperCache[T, V]`

`cache.Item` becomes `cache.Item[V any]`:

```go
type Item[V any] struct {
    Key         string
    Value       V
    LastAccess  time.Time
    // ...
}
```

`ConcurrentMap[V]`:

```go
type ConcurrentMap[V any] struct { /* shards map[string]*Item[V] */ }
```

`IBackend[T, V]`:

```go
type IBackend[T IBackendConstrain, V any] interface {
    Get(ctx context.Context, key string) (*Item[V], bool)
    Set(ctx context.Context, item *Item[V]) error
    // ...
}
```

`HyperCache[T, V]`. Two type parameters everywhere.

**Pros**:

- **Fully type-safe.** No `any` in user-facing types.
- **Zero runtime cost.** Type assertions are gone.
- **Items can be specialized.** An `Item[[]byte]` skips reflection-based size estimation that today's `SetSize` uses; the fast paths in [pkg/cache/v2/item.go:117-143](../../pkg/cache/v2/item.go#L117-L143) become the only path.

**Cons**:

- **Massive breaking change.** Every public type that mentions Item or HyperCache changes shape.
- **Heterogeneous workloads regress.** `Item[any]` works but is awkward; users mixing types must write `HyperCache[T, any]` everywhere.
- **Pool complications.** `ItemPoolManager` becomes `ItemPoolManager[V]`; you need one pool per V. The DistMemory rebalancer (which moves Items between cache instances) must be V-typed too. Real plumbing pain.
- **Two type parameters.** `HyperCache[backend.InMemory, *Session]` is a mouthful; users will type-alias.
- **Backends that serialize (Redis) can't statically dispatch on V** without further machinery (V-typed serializers, registries). The boundary cost doesn't disappear — it moves.

### Option C — Generic on ConcurrentMap + HyperCache only; backends keep `any`

Mid-point: typed at the layers users see, untyped at the persistence boundary.

```go
type HyperCache[T IBackendConstrain, V any] struct {
    backend backend.IBackend[T] // backend stays IBackend[T], not [T, V]
    // ...
}

func (h *HyperCache[T, V]) Set(ctx context.Context, key string, value V, exp time.Duration) error {
    item := h.itemPool.Get()
    item.Value = value // V → any; loses type info at backend boundary
    return h.backend.Set(ctx, item)
}

func (h *HyperCache[T, V]) Get(ctx context.Context, key string) (V, bool) {
    var zero V

    item, ok := h.backend.Get(ctx, key)
    if !ok {
        return zero, false
    }

    v, ok := item.Value.(V)
    if !ok {
        return zero, false
    }

    return v, true
}
```

**Pros**:

- Typed user-facing API.
- Backends untouched (Redis serializer code unchanged).
- Less invasive than B.

**Cons**:

- **Same runtime cost as Option A** (type-assertion at HyperCache boundary).
- **Larger diff than A** for the same runtime behavior.
- **Doesn't enable the Item[V] specializations** that motivate B.

## Recommendation

**Two-phase**:

- **v2.x — Option A.** Ship `hypercache.Typed[V]` as a thin wrapper. Zero breaking changes. Captures 90% of the ergonomic win (no caller-side type asserts) at 10% of the cost.
- **v3.x — Option B**, conditional on usage signal from A.
        - If most v2.x users adopt `Typed[V]` and report friction, that's strong evidence for full generics.
        - If usage is mixed (many users still want untyped for heterogeneous keys), keep the wrapper as the recommended pattern and skip B.

This avoids spending the deep-generics budget speculatively. Option A is reversible; Option B is not.

## Implementation plan (Option A, v2.x)

1. Add `hypercache_typed.go` with `Typed[T, V]`. Wrap each public method on `HyperCache[T]` (Set, Get, GetOrSet, GetWithInfo, GetMultiple, Remove, Clear, List).
1. `GetMultiple` returns `map[string]V` and `map[string]error` — wrong-type entries land in the error map under a new sentinel `ErrTypeMismatch`.
1. Document the wrapper as the recommended access pattern in the package doc. Examples in [`__examples/typed/`](../../__examples/typed/) (new).
1. Tests: confirm the wrapper round-trips Set→Get cleanly for several V types ([]byte, struct, pointer, slice, map). Confirm cross-typed read returns miss without panic.

## Implementation plan (Option B, v3.0 — sketch only)

If we commit to v3:

1. Generate `pkg/cache/v3` parallel to v2 with `Item[V]` and `ConcurrentMap[V]`. Don't touch v2 — let it ship in parallel.
1. `IBackend[T, V]`: rewrite the four backends. InMemory and DistMemory keep typed Items; Redis-family backends accept `Item[V]` and serialize V via a `Serializer[V]` interface (callers register one per V they cache).
1. `HyperCache[T, V]`. The existing constructor `New[T](ctx, bm, config)` becomes `New[T, V](ctx, bm, config)`. Type alias `New[T] = New[T, any]` preserves the unchanged-API path for users who don't want V.
1. ItemPoolManager becomes `ItemPoolManager[V]`. One pool per `(V)` typedef.
1. CHANGELOG entry: explicit list of generic signatures users must update; codemod recipe with `gofmt -r`.

Estimated diff: 2-4 weeks of focused work + extensive test re-baselining. Don't sequence behind any other v3 work — make it the v3 anchor.

## Migration

- **v2.x users (Option A landed)**: no change required. Adopt `Typed[V]` opportunistically.
- **v2.x → v3 with full generics (Option B landed)**: every `*HyperCache[T]` becomes `*HyperCache[T, V]`; pick V (likely `any` for code that doesn't want to commit). For typed call sites that already used `Typed[V]`, drop the wrapper — the underlying API now matches.

## Risks

| Risk | Likelihood | Mitigation |
| ---- | ---------- | ---------- |
| Wrapper's silent type-mismatch-as-miss surprises users | Medium | Document loudly; provide `GetTyped` that returns `(V, error)` with `ErrTypeMismatch` for callers who need the distinction. |
| Users mix `Typed[V]` and untyped Get on the same key, get inconsistent reads | Low | Document as caller error; add an example showing the wrong pattern with a comment. |
| Deep generics (B) blow up compile time / binary size | Low | Go generics are largely zero-cost via stenciling; measure on a representative repo before committing to v3. |
| Serializer-per-V registry (B) explodes API surface | Medium | Provide a default reflection-based serializer for backwards compat; concrete V serializers are opt-in. |
| Eviction algorithms need updating (B) | Low | They don't read V; keep `IAlgorithm` `any`-based or take `*Item[any]`. Decoupling from V is the right call regardless. |

## Open questions

- **Should `Typed[V]` enforce V via a constraint?** E.g., require V to be `comparable` so the wrapper can compare against zero-V. Probably no — comparable rules out slices/maps which are common cache values.
- **`ErrTypeMismatch` shape**: should the wrapper's Get return `(V, bool, error)` or just `(V, bool)`? The latter matches the existing API (Get returns `(any, bool)`); the former is friendlier to callers who want to distinguish miss-vs-bad-type.
- **Coexistence with Phase-5d lifecycle ctx**: the wrapper passes ctx through unchanged; no interaction.
- **DistMemory replication of typed Items (B)**: serialized Item carries V's JSON shape; the receiving node decodes against the same V. Cross-version V drift is a wire-format concern (today's `any` covers it because reflection picks up the right shape; with V we'd want a versioned schema). Defer the schema-evolution story to its own RFC if/when B lands.

## Decision criteria

Land Option A if:

- It compiles.
- Round-trip tests pass for representative V types.
- Wrapper Get+Set adds ≤ 5 ns/op overhead vs untyped Get+Set.

Land Option B if:

- A has been in production for ≥ 1 release cycle.
- Adoption metric (e.g., GitHub-search, internal grep) shows ≥ 50% of new code uses `Typed[V]`.
- A v3-cut is otherwise on the roadmap (Item-embedded eviction from RFC 0001 is a natural travel companion).
