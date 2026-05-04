# Changelog

All notable changes to HyperCache are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [2.0.0] — 2026-05-04

A modernization release. The headline themes:

- Eviction is now sharded by default for concurrency-friendly throughput.
- The distributed-memory backend (`DistMemory`) gained body limits, TLS,
  bearer-token auth, lifecycle-context cancellation, and surfaced
  listener errors.
- A typed wrapper (`Typed[T, V]`) is available for compile-time
  type-safe access without the caller-side type assertions of the
  untyped API.
- The legacy `pkg/cache` v1 store and the `longbridgeapp/assert` test
  dependency are gone.

The full course-correction plan (Phase 0 baseline → Phase 6 file split,
plus Phase 5a–5e DistMemory hardening) is in commit history. The two
RFCs that informed the design decisions live under [docs/rfcs/](docs/rfcs/).

### Breaking changes

- **`pkg/cache` v1 removed.** All callers must use `pkg/cache/v2`.
- **`longbridgeapp/assert` test dependency removed.** Tests now use
  `stretchr/testify/require`. Internal test code only — no impact on
  library consumers, but downstream contributors authoring tests
  against this codebase must use `require`.
- **`sentinel.ErrMgmtHTTPShutdownTimeout` removed.**
  `ManagementHTTPServer.Shutdown` now calls `app.ShutdownWithContext`
  and returns the underlying ctx error directly. Callers comparing
  against the removed sentinel must switch to `errors.Is(err,
  context.DeadlineExceeded)` or equivalent.
- **Sharded eviction is default-on (32 shards).** Items no longer
  evict in strict global LRU/LFU order — the algorithm operates
  independently within each shard. Total capacity is honored within
  ±32 (one slot of slack per shard). Use `WithEvictionShardCount(1)`
  to restore strict-global ordering at the cost of single-mutex
  contention.
- **`hypercache.go` decomposed into 6 files** (`hypercache.go`,
  `hypercache_io.go`, `hypercache_eviction.go`,
  `hypercache_expiration.go`, `hypercache_dist.go`,
  `hypercache_construct.go`). No public API change; third-party
  patches against line numbers in the prior single-file layout will
  not apply.
- **`ManagementHTTPServer` constructor order fix.**
  `WithMgmtReadTimeout` and `WithMgmtWriteTimeout` previously mutated
  struct fields *after* `fiber.New` had locked in the defaults — the
  options were silent no-ops. Construction order is now correct, so
  any code relying on the silent no-op (e.g., setting absurd values
  knowing they would be ignored) will see those values take effect.

### Performance

Measurements on Apple M4 Pro, `go test -bench`, `count=5`, benchstat.
Full release snapshot captured in [bench-v2.0.0.txt](bench-v2.0.0.txt).

- **Per-shard atomic `Count`.** `BenchmarkConcurrentMap_Count`:
  53 → ~10 ns/op. `_CountParallel`: 1181 → ~13 ns/op. Eliminates the
  lock-storm that previously serialized on a single mutex during
  eviction-loop count checks.
- **Sharded eviction algorithm** (`pkg/eviction/sharded.go`).
  Replaces the global eviction-algorithm mutex with 32 per-shard
  mutexes routed by the same hash `ConcurrentMap` uses, so a key's
  data shard and eviction shard align (cache-locality on Set).
- **`iter.Seq2` migration** replacing channel-based `IterBuffered`.
  `BenchmarkConcurrentMap_All` (renamed from `_IterBuffered`):
  757µs → 26.5µs/op (-96.51%). Bytes/op: 1.73 MiB → 0 B/op.
  Allocs/op: 230 → 0. Eliminated 32 goroutines + 32 channels per
  iteration.
- **xxhash consolidation** (`pkg/cache/v2/hash.go`). Replaced inlined
  FNV-1a with `xxhash.Sum64String` folded to 32 bits.
  `BenchmarkConcurrentMap_GetShard`: 10.07 → 3.46 ns/op (-65.63%).
- **Sharded item-aware eviction was tried and rejected** per
  [RFC 0001](docs/rfcs/0001-backend-owned-eviction.md). The
  hypothesis (duplicate-map overhead is the bottleneck) was
  falsified — sharded contention dominates. Code removed; lessons
  preserved in the RFC for future contributors.

### Features

- **`hypercache.Typed[T, V]` wrapper** for compile-time type-safe
  cache access. Wraps an existing `HyperCache[T]`; multiple `Typed`
  views can share one underlying cache over disjoint keyspaces.
  Includes `Set`, `Get`, `GetTyped` (explicit `ErrTypeMismatch`),
  `GetWithInfo`, `GetOrSet`, `GetMultiple`, `Remove`, `Clear`. See
  [hypercache_typed.go](hypercache_typed.go) and
  [RFC 0002 Phase 1](docs/rfcs/0002-generic-item-typing.md). Phase 2
  (deep `Item[V]` generics) is v3 territory, conditional on adoption
  signal.
- **`WithDistHTTPLimits(DistHTTPLimits)` option** for the dist
  transport: server `BodyLimit` / `ReadTimeout` / `WriteTimeout` /
  `IdleTimeout` / `Concurrency`, plus client `ResponseLimit` /
  `ClientTimeout`. Defaults: 16 MiB request/response body cap, 5 s
  read/write/client timeout, 60 s idle, fiber's 256 KiB concurrency
  cap. Partial overrides honored — zero fields inherit defaults.
- **`WithDistHTTPAuth(DistHTTPAuth)` option** for bearer-token auth on
  `/internal/*` and `/health` (`Token` for the common case;
  `ServerVerify`/`ClientSign` hooks for JWT, mTLS-derived identity,
  HMAC, etc.). Constant-time token compare on the server side. The
  auto-created HTTP client signs every outgoing request with the
  same token. Mismatched-token peers are rejected with HTTP 401
  (`sentinel.ErrUnauthorized`).
- **TLS support** via `DistHTTPLimits.TLSConfig`. The server wraps
  its listener with `tls.NewListener`; the auto-created HTTP client
  attaches the same `*tls.Config` to its `Transport.TLSClientConfig`
  with ALPN forced to `http/1.1` (fiber/fasthttp doesn't speak h2).
  Same `*tls.Config` configures both sides — operators applying it
  consistently across the cluster get encrypted intra-cluster
  traffic out of the box. Plaintext peers handshake-fail.
- **Dist server lifecycle context** — `DistMemory.LifecycleContext()`
  exposes a context derived from the constructor's that is canceled
  on `Stop()`. Replaces the prior pattern where handlers captured
  the constructor's `context.Background()` and never observed
  cancellation. In-flight handlers and replica forwards see `Done()`
  the moment `Stop` is called.
- **`LastServeError()` accessor** on both `distHTTPServer` and
  `ManagementHTTPServer`. Replaces the prior `_ = serveErr` pattern
  that silently swallowed listener-loop crashes — operators can now
  surface the failure to logs/alerts.
- **`Stop()` goroutine-leak fix.** Both `distHTTPServer.stop` and
  `ManagementHTTPServer.Shutdown` now call
  `app.ShutdownWithContext(ctx)` directly instead of wrapping
  `app.Shutdown()` in a goroutine and racing it against ctx done
  (which leaked the goroutine when ctx fired first).
- **New sentinels:** `sentinel.ErrTypeMismatch`,
  `sentinel.ErrUnauthorized`.

### Internal

Worth surfacing for contributors:

- **v2 module layout** is the file split listed under "Breaking
  changes" above — readability win, no API change.
- **Test helpers** introduced under `tests/`:
  `tests/dist_cluster_helper.go::SetupInProcessCluster[RF]`,
  `tests/merkle_node_helper.go`,
  `pkg/backend/dist_memory_test_helpers.go::EnableHTTPForTest`
  (build tag `test`).
- **Lint discipline:** 35 `nolint` directives total across the repo,
  each with a one-line justification. golangci-lint v2.12.1 runs
  clean with `--build-tags test`.

### Removed

- `pkg/cache` v1 (see "Breaking changes").
- `longbridgeapp/assert` test dependency (see "Breaking changes").
- `sentinel.ErrMgmtHTTPShutdownTimeout` (see "Breaking changes").
- Experimental `WithItemAwareEviction` option / `IAlgorithmItemAware`
  interface / `LRUItemAware` / `ShardedItemAware` types — landed
  briefly during the RFC 0001 spike, then torn out per the RFC's
  own discipline when the perf gate failed. The
  [RFC document](docs/rfcs/0001-backend-owned-eviction.md) preserves
  the measurement and the lessons.

[Unreleased]: https://github.com/hyp3rd/hypercache/compare/v2.0.0...HEAD
[2.0.0]: https://github.com/hyp3rd/hypercache/releases/tag/v2.0.0
