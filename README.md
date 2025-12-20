# HyperCache

[![Go](https://github.com/hyp3rd/hypercache/actions/workflows/go.yml/badge.svg)][build-link] [![CodeQL](https://github.com/hyp3rd/hypercache/actions/workflows/codeql.yml/badge.svg)][codeql-link] [![golangci-lint](https://github.com/hyp3rd/hypercache/actions/workflows/golangci-lint.yml/badge.svg)][golangci-lint-link]

## Synopsis

HyperCache is a **thread-safe** **high-performance** cache implementation in `Go` that supports multiple backends with optional size limits, item expiration, and pluggable eviction algorithms. It can be used as a standalone cache (single process or distributed via Redis / Redis Cluster) or wrapped by the [service interface](./service.go) to decorate operations with middleware (logging, metrics, tracing, etc.).

It is optimized for performance and flexibility:

- Tunable expiration and eviction intervals (or fully proactive eviction when the eviction interval is set to `0`).
- Debounced & coalesced expiration trigger channel to avoid thrashing.
- Non-blocking manual `TriggerEviction(context.Context)` signal.
- Serializer‑aware memory accounting (item size reflects the backend serialization format when available).
- Multiple eviction algorithms with the ability to register custom ones.
- Multiple stats collectors (default histogram) and middleware hooks.

It ships with a default [histogram stats collector](./stats/stats.go) and several eviction algorithms. You can register new ones by implementing the [Eviction Algorithm interface](./eviction/eviction.go):

- [Recently Used (LRU) eviction algorithm](./eviction/lru.go)
- [The Least Frequently Used (LFU) algorithm](./eviction/lfu.go)
- [Cache-Aware Write-Optimized LFU (CAWOLFU)](./eviction/cawolfu.go)
- [The Adaptive Replacement Cache (ARC) algorithm](./eviction/arc.go) — Experimental (not enabled by default)
- [The clock eviction algorithm](./eviction/clock.go)

### Features

- Thread-safe & lock‑optimized (sharded map + worker pool)
- High-performance (low allocations on hot paths, pooled items, serializer‑aware sizing)
- Multiple backends (extensible):
            1. [In-memory](./pkg/backend/inmemory.go)
            1. [Redis](./pkg/backend/redis.go)
            1. [Redis Cluster](./pkg/backend/redis_cluster.go)
- Item expiration & proactive expiration triggering (debounced/coalesced)
- Background or proactive (interval = 0) eviction using pluggable algorithms
- Manual, non-blocking eviction triggering (`TriggerEviction()`)
- Maximum cache size (bytes) & capacity (item count) controls
- Serializer‑aware size accounting for consistent memory tracking across backends
- Stats collection (histogram by default) + pluggable collectors
- Middleware-friendly service wrapper (logging, metrics, tracing, custom)
- Zero-cost if an interval is disabled (tickers are only created when > 0)

### Optional Management HTTP API

You can enable a lightweight management HTTP server to inspect and control a running cache instance.

Add the option when creating the config:

```go
cfg := hypercache.NewConfig[backend.InMemory](constants.InMemoryBackend)
cfg.HyperCacheOptions = append(cfg.HyperCacheOptions,
    hypercache.WithManagementHTTP[backend.InMemory]("127.0.0.1:9090"),
)
hc, _ := hypercache.New(context.Background(), hypercache.GetDefaultManager(), cfg)
```

Endpoints (subject to change):

- GET /health – liveness check
- GET /stats – current stats snapshot
- GET /config – sanitized runtime config (now includes replication + virtual node settings when using DistMemory)
- GET /dist/metrics – distributed backend forwarding / replication counters (DistMemory only)
- GET /dist/owners?key=K – current ring owners (IDs) for key K (DistMemory only, debug)
- GET /internal/merkle – Merkle tree snapshot (DistMemory experimental anti-entropy)
- GET /internal/keys – Full key enumeration (debug / anti-entropy fallback; expensive)
- GET /cluster/members – membership snapshot (id, address, state, incarnation, replication factor, virtual nodes)
- GET /cluster/ring – ring vnode hashes (debug / diagnostics)
- POST /evict – trigger eviction cycle
- POST /trigger-expiration – trigger expiration scan
- POST /clear – clear all items

Bind to 127.0.0.1 by default and wrap with an auth function via `WithMgmtAuth` for production use.

## Installation

Install HyperCache:

```bash
go get -u github.com/hyp3rd/hypercache
```

### Performance

Sample benchmark output (numbers will vary by hardware / Go version):

```bash
goos: darwin
goarch: arm64
pkg: github.com/hyp3rd/hypercache/tests/benchmark
cpu: Apple M2 Pro
BenchmarkHyperCache_Get-12                        72005894          66.71 ns/op         0 B/op           0 allocs/op
BenchmarkHyperCache_Get_ProactiveEviction-12      71068249          67.22 ns/op         0 B/op           0 allocs/op
BenchmarkHyperCache_List-12                       36435114          129.5 ns/op        80 B/op           1 allocs/op
BenchmarkHyperCache_Set-12                        10365289          587.4 ns/op       191 B/op           3 allocs/op
BenchmarkHyperCache_Set_Proactive_Eviction-12      3264818           1521 ns/op       282 B/op           5 allocs/op
PASS
ok   github.com/hyp3rd/hypercache/tests/benchmark 26.853s
```

### Examples

To run the examples, use the following command:

```bash
make run-example group=eviction  # or any other example
```

For a complete list of examples, refer to the [examples](./examples/README.md) directory.

### Observability (OpenTelemetry)

HyperCache provides optional OpenTelemetry middleware for tracing and metrics.

- Tracing: wrap the service with `middleware.NewOTelTracingMiddleware` using a `trace.Tracer`.
- Metrics: wrap with `middleware.NewOTelMetricsMiddleware` using a `metric.Meter`.

Example wiring:

```go
svc := hypercache.ApplyMiddleware(svc,
    func(next hypercache.Service) hypercache.Service { return middleware.NewOTelTracingMiddleware(next, tracer) },
    func(next hypercache.Service) hypercache.Service { mw, _ := middleware.NewOTelMetricsMiddleware(next, meter); return mw },
)
```

Use your preferred OpenTelemetry SDK setup for exporters and processors in production; the snippet uses no-op providers for simplicity.

### Eviction algorithms

Available algorithm names you can pass to `WithEvictionAlgorithm`:

- "lru" — Least Recently Used (default)
- "lfu" — Least Frequently Used (with LRU tie-breaker for equal frequencies)
- "clock" — Second-chance clock
- "cawolfu" — Cache-Aware Write-Optimized LFU
- "arc" — Adaptive Replacement Cache (experimental; not registered by default)

Note: ARC is experimental and isn’t included in the default registry. If you choose to use it, register it manually or enable it explicitly in your build.

## API

`NewInMemoryWithDefaults(ctx, capacity)` is the quickest way to start:

```golang
cache, err := hypercache.NewInMemoryWithDefaults(context.Background(), 100)
if err != nil {
    // handle error
}
```

For fine‑grained control use `NewConfig` + `New`:

```golang
config := hypercache.NewConfig[backend.InMemory](constants.InMemoryBackend)
config.HyperCacheOptions = []hypercache.Option[backend.InMemory]{
    hypercache.WithEvictionInterval[backend.InMemory](time.Minute * 5),
    hypercache.WithEvictionAlgorithm[backend.InMemory]("cawolfu"),
    hypercache.WithExpirationTriggerDebounce[backend.InMemory](250 * time.Millisecond),
    hypercache.WithMaxEvictionCount[backend.InMemory](100),
    hypercache.WithMaxCacheSize[backend.InMemory](64 * 1024 * 1024), // 64 MiB logical cap
}
config.InMemoryOptions = []backend.Option[backend.InMemory]{ backend.WithCapacity[backend.InMemory](10) }

cache, err := hypercache.New(context.Background(), hypercache.GetDefaultManager(), config)
if err != nil {
    fmt.Fprintln(os.Stderr, err)
    return
}
```

### Advanced options quick reference

| Option | Purpose |
|--------|---------|
| `WithEvictionInterval` | Periodic eviction loop; set to `0` for proactive per-write eviction. |
| `WithExpirationInterval` | Periodic scan for expired items. |
| `WithExpirationTriggerBuffer` | Buffer size for coalesced expiration trigger channel. |
| `WithExpirationTriggerDebounce` | Drop rapid-fire triggers within a window. |
| `WithEvictionAlgorithm` | Select eviction algorithm (lru, lfu, clock, cawolfu, arc*). |
| `WithMaxEvictionCount` | Cap number of items evicted per cycle. |
| `WithMaxCacheSize` | Max cumulative serialized item size (bytes). |
| `WithStatsCollector` | Choose stats collector implementation. |
| `WithManagementHTTP` | Start optional management HTTP server. |
| `WithDistReplication` | (DistMemory) Set replication factor (owners per key). |
| `WithDistVirtualNodes` | (DistMemory) Virtual nodes per physical node for consistent hashing. |
| `WithDistMerkleChunkSize` | (DistMemory) Keys per Merkle leaf chunk (power-of-two recommended). |
| `WithDistMerkleAutoSync` | (DistMemory) Interval for background Merkle sync (<=0 disables). |
| `WithDistMerkleAutoSyncPeers` | (DistMemory) Limit peers synced per auto-sync tick (0=all). |
| `WithDistListKeysCap` | (DistMemory) Cap number of keys fetched via fallback enumeration. |
| `WithDistNode` | (DistMemory) Explicit node identity (id/address). |
| `WithDistSeeds` | (DistMemory) Static seed addresses to pre-populate membership. |
| `WithDistTombstoneTTL` | (DistMemory) Retain delete tombstones for this duration before compaction (<=0 = infinite). |
| `WithDistTombstoneSweep` | (DistMemory) Interval to run tombstone compaction (<=0 disables). |

*ARC is experimental (not registered by default).

### Redis / Redis Cluster notes

When using Redis or Redis Cluster, item size accounting uses the configured serializer (e.g. msgpack) to align in-memory and remote representations. Provide the serializer via backend options (`WithSerializer` / `WithClusterSerializer`).

**Refer to [config.go](./config.go) for complete option definitions and the GoDoc on pkg.go.dev for an exhaustive API reference.**

## Usage

### Distributed In‑Process Backend (Experimental)

The experimental in‑process distributed backend `DistMemory` (feature branch: `feat/distributed-backend`) simulates a small cluster inside a single process using a consistent hash ring with virtual nodes, replication, quorum consistency, forwarding, replica fan‑out, rebalancing, hinted handoff, tombstones and Merkle-based anti‑entropy.

A detailed deep dive lives in [docs/distributed.md](./docs/distributed.md). Below is a concise summary.

Current capabilities (implemented):

- Static membership + ring with configurable replication factor & virtual nodes.
- Ownership enforcement (non‑owners forward to primary; promotion if primary unreachable).
- Replica fan‑out on writes + replica removals.
- Quorum reads & writes (ONE / QUORUM / ALL) with read repair.
- Hinted handoff (TTL, replay, per-node + global caps, metrics).
- Delete semantics with versioned tombstones (TTL + compaction, anti‑resurrection guard).
- Merkle tree anti‑entropy (build/diff/pull) + metrics.
- Periodic auto Merkle sync (peer cap optional).
- Heartbeat-based failure detection (alive→suspect→dead) + metrics.
- Lightweight gossip snapshot exchange (in-process only).
- Rebalancing (primary change & lost ownership migrations) with batching and concurrency throttling metrics.
- Latency histograms for Get/Set/Remove.

Limitations / not yet implemented:

- Replica-only ownership diff migrations.
- Full gossip-based dynamic membership & indirect probing.
- Advanced versioning (HLC / vector clocks).
- Tracing spans for distributed operations.
- Security (TLS/mTLS, auth) & compression.
- Persistence / durability (out of scope presently).

#### Rebalancing & Ownership Migration (Experimental Phase 3)

The DistMemory backend includes an experimental periodic rebalancer that:

- Scans local shards each tick (interval configurable via `WithDistRebalanceInterval`).
- Collects candidate keys when this node either (a) is no longer an owner (primary or replica) or (b) was the recorded primary and the current primary changed.
- Migrates candidates in batches (`WithDistRebalanceBatchSize`) with bounded parallelism (`WithDistRebalanceMaxConcurrent`).
- Uses a semaphore; saturation increments the `RebalanceThrottle` metric.

Migration is best‑effort (fire‑and‑forget forward of the item to the new primary); failures are not yet retried or queued. Owner set diffing now covers:

- Primary change & full ownership loss (migrate off this node).
- Replica-only additions (push current value to newly added replicas; capped by `WithDistReplicaDiffMaxPerTick`).

Replica removal cleanup (actively dropping data from nodes no longer replicas) is pending.

Metrics (via management or `Metrics()`):

| Metric | Description |
|--------|-------------|
| RebalancedKeys | Count of all rebalance-related migrations (primary changes + replica diff replications). |
| RebalancedPrimary | Count of primary ownership change migrations (subset of RebalancedKeys). |
| RebalanceBatches | Number of migration batches executed. |
| RebalanceThrottle | Times migration concurrency limiter saturated. |
| RebalanceLastNanos | Duration (ns) of last rebalance scan. |
| RebalancedReplicaDiff | Count of replica-only diff replications (new replicas seeded). |
| RebalanceReplicaDiffThrottle | Times replica-only diff processing hit per-tick cap. |

Test helpers `AddPeer` and `RemovePeer` simulate join / leave events that trigger redistribution in integration tests (`dist_rebalance_*.go`).

### Roadmap / PRD Progress Snapshot

| Area | Status |
|------|--------|
| Core in-process sharding | Done |
| Replication fan-out | Done |
| Read-repair | Done |
| Quorum consistency (R/W) | Done |
| Hinted handoff (TTL, replay, caps) | Done |
| Tombstones (TTL + compaction) | Done |
| Merkle anti-entropy (pull) | Done |
| Merkle phase metrics | Done |
| Auto Merkle background sync | Done |
| Rebalancing (primary/lost ownership + replica-add diff + shedding) | Partial (shedding grace delete added; future retry/queue) |
| Failure detection (heartbeat) | Partial (basic) |
| Lightweight gossip snapshot | Partial |
| Replica-only migration diff | Planned |
| Adaptive Merkle scheduling | Planned |
| Advanced versioning (HLC/vector) | Planned |
| Client SDK (direct routing) | Planned |
| Tracing spans | Planned |
| Security (TLS/auth) | Planned |
| Compression | Planned |
| Persistence | Out of scope (current phase) |
| Chaos / fault injection | Planned |
| Benchmarks & tests | Ongoing (broad coverage) |

Example minimal setup:

```go
cfg := hypercache.NewConfig[backend.DistMemory](constants.DistMemoryBackend)
cfg.HyperCacheOptions = append(cfg.HyperCacheOptions,
    hypercache.WithManagementHTTP[backend.DistMemory]("127.0.0.1:9090"),
)
cfg.DistMemoryOptions = []backend.Option[backend.DistMemory]{
    backend.WithDistReplication(3),
    backend.WithDistVirtualNodes(64),
    backend.WithDistNode("node-a", "127.0.0.1:7000"),
    backend.WithDistSeeds([]string{"127.0.0.1:7001", "127.0.0.1:7002"}),
}
hc, err := hypercache.New(context.Background(), hypercache.GetDefaultManager(), cfg)
if err != nil { log.Fatal(err) }
defer hc.Stop(context.Background())
```

Note: DistMemory is not a production distributed cache; it is a stepping stone towards a networked, failure‑aware implementation.

#### Consistency & Quorum Semantics

DistMemory currently supports three consistency levels configurable independently for reads and writes:

- ONE: Return after the primary (or first reachable owner) succeeds.
- QUORUM: Majority of owners (floor(R/2)+1) must acknowledge.
- ALL: Every owner must acknowledge; any unreachable replica causes failure.

Required acknowledgements are computed at runtime from the ring's current replication factor. For writes, the primary applies locally then synchronously fans out to remaining owners; for reads, it queries owners until the required number of successful responses is achieved (promoting next owner if a primary is unreachable). Read‑repair occurs when a later owner returns a newer version than the local primary copy.

#### Hinted Handoff

When a replica is unreachable during a write, a hint (deferred write) is enqueued locally keyed by the target node ID. Hints have a TTL (`WithDistHintTTL`) and are replayed on an interval (`WithDistHintReplayInterval`). Limits can be applied per node (`WithDistHintMaxPerNode`) as well as globally across all nodes (`WithDistHintMaxTotal` total entries, `WithDistHintMaxBytes` approximate bytes). Expired hints are dropped; delivered hints increment replay counters; globally capped drops increment a separate metric. Metrics exposed via the management endpoint allow monitoring queued, replayed, expired, dropped (transport errors), and globally dropped hints along with current approximate queued bytes.

Test helper methods for forcing a replay cycle (`StartHintReplayForTest`, `ReplayHintsForTest`, `HintedQueueSize`) are compiled only under the `test` build tag to keep production binaries clean.

To run tests that rely on these helpers:

```bash
go test -tags test ./...
```

#### Build Tags

The repository uses a `//go:build test` tag to include auxiliary instrumentation and helpers exclusively in test builds (e.g. hinted handoff queue inspection). Production builds omit these symbols automatically. Heartbeat peer sampling (`WithDistHeartbeatSample`) and membership state metrics (suspect/dead counters) are part of the experimental failure detection added in Phase 2.

#### Metrics Snapshot

The `/dist/metrics` endpoint (and `DistMemory.Metrics()` API) expose counters for forwarding operations, replica fan‑out, read‑repair, hinted handoff lifecycle, quorum write attempts/acks/failures, Merkle sync timings, tombstone activity, and heartbeat probes. These are reset only on process restart.

#### Future Evolution (Selected)

Prioritized next steps (see `docs/distributed.md` for full context):

- Replica-only ownership diff & migration (push of newly added replicas implemented; removal/cleanup pending).
- Migration retry queue & success/failure metrics.
- Adaptive / incremental Merkle scheduling.
- Client SDK with direct owner hashing.
- Tracing spans for distributed ops (Set, Get, Repair, Merkle, Rebalance, HintReplay).
- Enhanced failure detection (indirect probes, gossip dissemination).
- Security (TLS/mTLS) + auth middleware.
- Chaos & latency / fault injection hooks.

DistMemory remains experimental; treat interfaces as unstable until promoted out of the feature branch.

Examples can be too broad for a readme, refer to the [examples](./__examples/README.md) directory for a more comprehensive overview.

## License

The code and documentation in this project are released under Mozilla Public License 2.0.

## Author

I'm a surfer, and a software architect with 15 years of experience designing highly available distributed production systems and developing cloud-native apps in public and private clouds. Feel free to connect with me on LinkedIn.

[![LinkedIn](https://img.shields.io/badge/LinkedIn-0077B5?style=for-the-badge&logo=linkedin&logoColor=white)](https://www.linkedin.com/in/francesco-cosentino/)

[build-link]: https://github.com/hyp3rd/hypercache/actions/workflows/go.yml
[codeql-link]:https://github.com/hyp3rd/hypercache/actions/workflows/codeql.yml
[golangci-lint-link]:https://github.com/hyp3rd/hypercache/actions/workflows/golangci-lint.yml
