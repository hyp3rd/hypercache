# Changelog

All notable changes to HyperCache are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- **Cluster propagation was completely broken.** The
  `DistMemoryBackendConstructor.Create` factory in `factory.go`
  silently discarded `cfg.DistMemoryOptions` and called
  `backend.NewDistMemory(ctx)` with **no arguments**. Every
  `WithDistNode`, `WithDistSeeds`, `WithDistReplication`, etc. that
  callers wired through `hypercache.NewConfig` was a silent no-op,
  leaving every node with a default standalone configuration that
  only knew itself. The factory now forwards
  `cfg.DistMemoryOptions...` like every other backend constructor
  does. This was the production-blocking bug — a Set on one node
  never reached its peers because the other nodes weren't actually
  in any node's ring.
- **Seed addresses without node IDs produced a broken ring.**
  `initStandaloneMembership` added every seed to membership with an
  empty `NodeID`, so the consistent-hash ring was built over
  empty-string owners. `Set` would resolve owners as
  `["", "", "self"]`, fan-outs to `""` failed with
  `ErrBackendNotFound`, the writer self-promoted, and the data
  never reached its peers. The HTTP transport has no node-discovery
  protocol, so the only way to populate node IDs in the ring is at
  configuration time. Seeds now accept an optional `id@addr` syntax
  (`node-2@hypercache-2:7946`) — bare `addr` keeps the legacy
  empty-ID behavior for in-process tests. Production deployments
  must use `id@addr`.
- **`Remove` from a non-primary owner skipped the primary.**
  `removeImpl` checked `dm.ownsKeyInternal(key)` (true for any
  ring owner) and ran `applyRemove` locally — but `applyRemove`'s
  fan-out only covers `owners[1:]` under the assumption the caller
  is `owners[0]`. When a replica initiated the remove, the primary
  never got the delete. The Remove path now mirrors Set:
  non-primary callers forward to the primary, primary applies +
  fans out. Tombstones now propagate cluster-wide regardless of
  which node receives the DELETE.
- **Client API responses were unhelpful.** Set/Remove returned
  `204 No Content` with empty bodies; errors were raw text via
  `SendString`. Replaced with structured JSON: PUT/DELETE return
  `{key, stored|deleted, bytes, node, owners}` so operators can
  immediately see where the value landed; errors return
  `{error, code}` with stable code strings (`BAD_REQUEST`,
  `NOT_FOUND`, `DRAINING`, `INTERNAL`). Added
  `GET /v1/owners/:key` for client-side ring visibility.
- **GET response leaked base64 on replicas.** `[]byte` values
  round-trip through JSON as base64 strings; replica nodes that
  received a value via the dist HTTP transport stored it as a
  `string` and returned it raw, so a `PUT world` on node-A
  resulted in `d29ybGQ=` from `GET` on node-B. The client GET
  handler now base64-decodes string values when they look like
  valid byte content, restoring writer-receiver symmetry.
- **GET on non-owner nodes returned a JSON-quoted base64 string.**
  The dist HTTP transport's `decodeGetBody` decodes `Item.Value` as
  `json.RawMessage` to preserve wire-bytes type fidelity. The
  client GET handler's type switch only matched `[]byte` and
  `string`, so non-owner GETs (which always go through the
  forward-fetch path) fell to the `default` branch and re-emitted
  the value as JSON — producing `"d29ybGQ="` instead of `world`.
  Added an explicit `json.RawMessage` case that interprets the raw
  JSON as a string when possible, then base64-decodes if applicable.
  Verified end-to-end against the 5-node Docker cluster where two
  of the five nodes are non-owners for any given key.

- **Race in `queueHint` between hint enqueue and hint replay.** Pre-fix,
  the metric write `dm.metrics.hintedBytes.Store(dm.hintBytes)` happened
  *after* releasing `hintsMu`, so a concurrent `adjustHintAccounting`
  call from the replay loop could race the read. Capturing the value
  under the lock closes the race. Surfaced when migration failures
  began funneling through `queueHint` (Phase B.2 below) — previously
  the migration path swallowed errors silently, so the hint enqueue
  rate from rebalance ticks was much lower.

### Added

- **Structured logging on the dist backend.** New `WithDistLogger(*slog.Logger)`
  option wires a structured logger into the dist backend's background
  loops (heartbeat, hint replay, rebalance, merkle sync) and operational
  error surfaces (HTTP listener bind failures, serve-goroutine exits,
  failed migrations during rebalance, dropped hints, peer state
  transitions). Library default is silent — `WithDistLogger` not called
  installs a `slog.DiscardHandler` so the dist backend never writes to
  stderr unless the caller opts in. Every record is pre-bound with
  `component=dist_memory` and `node_id=<id>` attributes for grep/filter.
  Phase A.1 of the production-readiness work.
- **OpenTelemetry tracing on the dist backend.** New
  `WithDistTracerProvider(trace.TracerProvider)` option opens spans on
  every public `Get` / `Set` / `Remove`, with child spans
  (`dist.replicate.set` / `dist.replicate.remove`) per peer during
  fan-out. Span attributes include `cache.key.length`,
  `dist.consistency`, `dist.owners.count`, `dist.acks`, `cache.hit`,
  and `peer.id`. Cache key *values* are intentionally never recorded
  on spans — keys can be PII (user IDs, session tokens). Library
  default is a no-op tracer (`noop.NewTracerProvider`), so spans cost
  nothing unless the caller opts in. New `ConsistencyLevel.String()`
  method renders consistency levels human-readably for log/span attrs.
  Phase A.2 of the production-readiness work.
- **OpenTelemetry metrics on the dist backend.** New
  `WithDistMeterProvider(metric.MeterProvider)` option registers an
  observable instrument for every field on `DistMetrics` — counters
  for cumulative totals (`dist.write.attempts`, `dist.forward.*`,
  `dist.hinted.*`, `dist.merkle.syncs`, `dist.rebalance.*`, etc.),
  gauges for current state (`dist.members.alive`,
  `dist.tombstones.active`, `dist.hinted.bytes`, last-operation
  latencies in nanoseconds, etc.). A single registered callback
  observes all instruments from one `Metrics()` snapshot per
  collection cycle, so there is no per-operation overhead beyond the
  existing atomic counters. Names use the `dist.` prefix so a
  Prometheus exporter renders them under a single subsystem.
  `Stop` unregisters the callback so the SDK does not invoke it
  against a stopped backend. Library default is a no-op meter, so
  metrics cost nothing unless the caller opts in. Phase A.3 of the
  production-readiness work.
- **SWIM-style indirect heartbeat probes.** New
  `WithDistIndirectProbes(k, timeout)` option enables the indirect-
  probe refutation path: when a direct heartbeat to a peer fails,
  this node asks `k` random alive peers to probe the target on its
  behalf, and only marks the target suspect if every relay also
  fails. Filters caller-side network blips (transient NIC reset,
  single stuck connection in this node's pool) that would otherwise
  cause spurious suspect/dead transitions. New transport method
  `IndirectHealth(ctx, relayNodeID, targetNodeID)` and HTTP endpoint
  `GET /internal/probe?target=<id>` carry the probe; auth-wrapped
  identically to the rest of `/internal/*`. New metrics
  `dist.heartbeat.indirect_probe.success`, `.failure`, `.refuted`
  expose probe outcomes. `k = 0` (default) preserves the pre-Phase-B
  behavior. Phase B.1 of the production-readiness work — note that
  the heartbeat path still carries the `experimental` marker until
  self-refutation via incarnation-disseminating gossip lands in a
  later phase.
- **Migration failures now retry through the hint queue.** When a
  rebalance forwards a key to its new primary and the transport
  returns *any* error (not just `ErrBackendNotFound`), the item is
  enqueued onto the existing hint-replay queue keyed by the new
  primary, instead of being logged and dropped. The hint-replay
  loop drains it on its configured schedule until the hint TTL
  expires. Same broadening applies to the `replicateTo` fan-out on
  the primary `Set` path — transient HTTP failures (timeout, 5xx,
  connection reset) no longer silently drop replicas. Phase B.2 of
  the production-readiness work.
- **On-wire compression for the dist HTTP transport.** New
  `DistHTTPLimits.CompressionThreshold` field opts the auto-created
  HTTP client into gzip-compressing Set request bodies whose
  serialized payload exceeds the configured byte threshold. The
  client sets `Content-Encoding: gzip` and the server transparently
  decompresses (via fiber v3's auto-decoding `Body()`). Threshold
  `0` (default) preserves the pre-Phase-B wire format byte-for-byte.
  Operators on bandwidth-constrained links with values above ~1 KiB
  typically see meaningful reductions; below-threshold values pay
  no compression cost. Roll out the threshold to all peers before
  raising it on any peer — a server with compression disabled will
  reject a gzip body with HTTP 400. Phase B.3 of the
  production-readiness work.
- **Drain endpoint for graceful shutdown.** New
  `DistMemory.Drain(ctx)` method and `POST /dist/drain` HTTP
  endpoint mark the node for shutdown: `/health` returns 503 so
  load balancers stop routing, `Set`/`Remove` return
  `sentinel.ErrDraining`, `Get` continues to serve so in-flight
  reads complete. New `IsDraining()` accessor for dashboards. New
  metric `dist.drains` records transitions. Drain is one-way and
  idempotent. Phase C.1 of the production-readiness work.
- **Cursor-based key enumeration** replaces the pre-Phase-C
  testing-only `/internal/keys` endpoint. The endpoint now returns
  shard-level pages with a `next_cursor` token; clients walk the
  cursor chain to enumerate the full key set. New `?limit=<n>` query
  parameter truncates within a shard for clusters with very large
  shards (response then carries `truncated=true` and the same
  `next_cursor`). The `DistHTTPTransport.ListKeys` helper now walks
  pages internally so existing callers (anti-entropy fallback, tests)
  keep their full-set semantics unchanged. Phase C.2 of the
  production-readiness work.
- **Operations runbook** at [docs/operations.md](docs/operations.md)
  covering split-brain, hint-queue overflow, rebalance under load,
  replica loss, observability wiring (logger/tracer/meter), drain
  procedure, and capacity-planning notes. Cross-links each failure
  mode to the metrics that surface it. Phase C.3 of the
  production-readiness work.
- **Production server binary** at
  [`cmd/hypercache-server`](cmd/hypercache-server). Wraps DistMemory
  via HyperCache and exposes three HTTP listeners per node: the
  client REST API (`PUT`/`GET`/`DELETE /v1/cache/:key`),
  management HTTP (`/health`, `/stats`, `/config`,
  `/dist/metrics`, `/cluster/*`), and the inter-node dist HTTP.
  12-factor configuration via `HYPERCACHE_*` environment
  variables — same binary runs in Docker, k8s, and bare-metal.
  Graceful shutdown on SIGTERM/SIGINT runs Drain → API stop →
  HyperCache Stop with a 30 s deadline. JSON-formatted slog
  logger pre-bound with `node_id`. Multi-stage `Dockerfile` builds
  a distroless static image (`gcr.io/distroless/static-debian12:nonroot`).
- **5-node local cluster compose** at
  [`docker-compose.cluster.yml`](docker-compose.cluster.yml) — five
  hypercache-server nodes on a shared `hypercache-cluster` Docker
  network, each knowing the other four as seeds, replication=3.
  Client APIs exposed on host ports 8081–8085, management HTTP on
  9081–9085. Includes a smoke-test recipe in the
  [server README](cmd/hypercache-server/README.md). Phase D of the
  production-readiness work.
- **`HyperCache.DistDrain(ctx)`** convenience method in
  [hypercache_dist.go](hypercache_dist.go) — calls Drain on the
  underlying DistMemory backend when one is configured, no-op on
  in-memory / Redis backends. Lets the server binary trigger drain
  without type-asserting through the unexported backend field.

## [0.5.0] — 2026-05-05

### Security

- **Fixed silent inbound auth bypass when `DistHTTPAuth.ClientSign` was
  set without a matching inbound verifier.** Previously, a config of
  `DistHTTPAuth{ClientSign: hmacSign}` flipped the internal `configured`
  predicate to true (causing the auto-client to sign outbound traffic),
  but `verify()` had no inbound material and silently allowed every
  request — so an operator wiring half of an HMAC scheme could end up
  with signed-out / open-in nodes that looked authenticated. The
  internal predicate is now split into `inboundConfigured()` /
  outbound-path checks, and `NewDistMemory` rejects this shape at
  construction with `sentinel.ErrInsecureAuthConfig`. Operators who
  legitimately want signed-out / open-in deployments (e.g. inbound is
  gated by an L4 firewall or service mesh below this server) must opt
  in via the new `DistHTTPAuth.AllowAnonymousInbound` field. All other
  configurations (`Token`-only, `Token+ServerVerify`, `Token+ClientSign`,
  `ServerVerify`-only) are unaffected. Reported by the post-tag
  security review; addressed before any v0.5.0 public announcement.

### Added

- `DistHTTPAuth.AllowAnonymousInbound` — explicit opt-in for asymmetric
  signed-out / open-in configurations.
- `sentinel.ErrInsecureAuthConfig` — surfaced from `NewDistMemory` when
  the auth policy would silently disable inbound enforcement.

## [0.4.3] — 2026-05-04

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

Unreleased: <https://github.com/hyp3rd/hypercache/compare/v0.5.0...HEAD>
Released: [0.5.0](https://github.com/hyp3rd/hypercache/releases/tag/v0.5.0)
