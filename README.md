# HyperCache

[![Go](https://github.com/hyp3rd/hypercache/actions/workflows/go.yml/badge.svg)][build-link] [![CodeQL](https://github.com/hyp3rd/hypercache/actions/workflows/codeql.yml/badge.svg)][codeql-link] [![golangci-lint](https://github.com/hyp3rd/hypercache/actions/workflows/golangci-lint.yml/badge.svg)][golangci-lint-link]

## Synopsis

HyperCache is a **thread-safe** **high-performance** cache implementation in `Go` that supports multiple backends with optional size limits, item expiration, and pluggable eviction algorithms. It can be used as a standalone cache (single process or distributed via Redis / Redis Cluster) or wrapped by the [service interface](./service.go) to decorate operations with middleware (logging, metrics, tracing, etc.).

It is optimized for performance and flexibility:

- Tunable expiration and eviction intervals (or fully proactive eviction when the eviction interval is set to `0`).
- Debounced & coalesced expiration trigger channel to avoid thrashing.
- Non-blocking manual `TriggerEviction()` signal.
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
    2. [Redis](./pkg/backend/redis.go)
    3. [Redis Cluster](./pkg/backend/redis_cluster.go)
- Item expiration & proactive expiration triggering (debounced/coalesced)
- Background or proactive (interval = 0) eviction using pluggable algorithms
- Manual, non-blocking eviction triggering (`TriggerEviction()`)
- Maximum cache size (bytes) & capacity (item count) controls
- Serializer‑aware size accounting for consistent memory tracking across backends
- Stats collection (histogram by default) + pluggable collectors
- Middleware-friendly service wrapper (logging, metrics, tracing, custom)
- Zero-cost if an interval is disabled (tickers are only created when > 0)

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

`NewInMemoryWithDefaults(capacity)` is the quickest way to start:

```golang
cache, err := hypercache.NewInMemoryWithDefaults(100)
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

cache, err := hypercache.New(hypercache.GetDefaultManager(), config)
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

*ARC is experimental (not registered by default).

### Redis / Redis Cluster notes

When using Redis or Redis Cluster, item size accounting uses the configured serializer (e.g. msgpack) to align in-memory and remote representations. Provide the serializer via backend options (`WithSerializer` / `WithClusterSerializer`).

**Refer to [config.go](./config.go) for complete option definitions and the GoDoc on pkg.go.dev for an exhaustive API reference.**

## Usage

Examples can be too broad for a readme, refer to the [examples](./__examples/README.md) directory for a more comprehensive overview.

## License

The code and documentation in this project are released under Mozilla Public License 2.0.

## Author

I'm a surfer, and a software architect with 15 years of experience designing highly available distributed production systems and developing cloud-native apps in public and private clouds. Feel free to connect with me on LinkedIn.

[![LinkedIn](https://img.shields.io/badge/LinkedIn-0077B5?style=for-the-badge&logo=linkedin&logoColor=white)](https://www.linkedin.com/in/francesco-cosentino/)

[build-link]: https://github.com/hyp3rd/hypercache/actions/workflows/go.yml
[codeql-link]:https://github.com/hyp3rd/hypercache/actions/workflows/codeql.yml
[golangci-lint-link]:https://github.com/hyp3rd/hypercache/actions/workflows/golangci-lint.yml
