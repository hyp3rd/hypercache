# HyperCache

[![Go](https://github.com/hyp3rd/hypercache/actions/workflows/go.yml/badge.svg)][build-link] [![CodeQL](https://github.com/hyp3rd/hypercache/actions/workflows/codeql.yml/badge.svg)][codeql-link] [![golangci-lint](https://github.com/hyp3rd/hypercache/actions/workflows/golangci-lint.yml/badge.svg)][golangci-lint-link]

## Synopsis

HyperCache is a **thread-safe** **high-performance** cache implementation in `Go` that supports multiple backends with an optional size limit, expiration, and eviction of items with custom algorithms alongside the defaults. It can be used as a standalone cache, distributed environents, or as a cache middleware for a service. It can implement a [service interface](./service.go) to intercept and decorate the cache methods with middleware (default or custom).
It is optimized for performance and flexibility, allowing to specify of the expiration and eviction intervals and providing and registering new eviction algorithms, stats collectors, and middleware(s).
It ships with a default [historigram stats collector](./pkg/stats/stats.go) and several eviction algorithms. However, you can develop and register your own if it implements the [Eviction Algorithm interface](./pkg/eviction/eviction.go).:

- [Recently Used (LRU) eviction algorithm](./pkg/eviction/lru.go)
- [The Least Frequently Used (LFU) algorithm](./pkg/eviction/lfu.go)
- [Cache-Aware Write-Optimized LFU (CAWOLFU)](./pkg/eviction/cawolfu.go)
- [The Adaptive Replacement Cache (ARC) algorithm](./pkg/eviction/arc.go) — Experimental (not enabled by default)
- [The clock eviction algorithm](./pkg/eviction/clock.go)

### Features

- Thread-safe
- High-performance
- Supports multiple, custom backends. Default backends are:
    1. [In-memory](./pkg/backend/inmemory.go)
    2. [Redis](./pkg/backend/redis.go)
- Store items in the cache with a key and expiration duration
- Retrieve items from the cache by their key
- Delete items from the cache by their key
- Clear the cache of all items
- Evict items in the background based on the cache capacity and items access leveraging several custom eviction algorithms
- Expire items in the background based on their duration
- [Eviction Algorithm interface](./pkg/eviction/eviction.go) to implement custom eviction algorithms.
- Stats collection with a default [stats collector](./pkg/stats/stats.go) or a custom one that implements the StatsCollector interface.
- [Service interface implementation](./service.go) to allow intercepting cache methods and decorate them with custom or default middleware(s).

## Installation

Install HyperCache:

```bash
go get -u github.com/hyp3rd/hypercache
```

### performance

Running the benchmarks on a 2019 MacBook Pro with a 2.4 GHz 8-Core Intel Core i9 processor and 32 GB 2400 MHz DDR4 memory, the results are as follows on average, using a pretty busy machine:

```bash
make bench
cd tests/benchmark && go test -bench=. -benchmem -benchtime=4s . -timeout 30m
goos: darwin
goarch: amd64
pkg: github.com/hyp3rd/hypercache/tests/benchmark
cpu: Intel(R) Core(TM) i9-9880H CPU @ 2.30GHz
BenchmarkHyperCache_Get-16                          37481116           115.7 ns/op         0 B/op          0 allocs/op
BenchmarkHyperCache_Get_ProactiveEviction-16        39486261           116.2 ns/op         0 B/op          0 allocs/op
BenchmarkHyperCache_List-16                         11299632           412.0 ns/op        85 B/op          1 allocs/op
BenchmarkHyperCache_Set-16                           2765406          1556 ns/op         248 B/op          4 allocs/op
BenchmarkHyperCache_Set_Proactive_Eviction-16        2947629          1700 ns/op         162 B/op          3 allocs/op
PASS
ok      github.com/hyp3rd/hypercache/tests/benchmark    30.031s
```

### Examples

To run the examples, use the following command:

```bash
make run-example group=eviction  # or any other example
```

For a complete list of examples, refer to the [examples](./__examples/README.md) directory.

### Observability (OpenTelemetry)

HyperCache provides optional OpenTelemetry middleware for tracing and metrics.

- Tracing: wrap the service with `middleware.NewOTelTracingMiddleware` using a `trace.Tracer`.
- Metrics: wrap with `middleware.NewOTelMetricsMiddleware` using a `metric.Meter`.

Example wiring (see `__examples/observability/otel.go`):

```go
svc := hypercache.ApplyMiddleware(svc,
    func(next hypercache.Service) hypercache.Service { return middleware.NewOTelTracingMiddleware(next, tracer) },
    func(next hypercache.Service) hypercache.Service { mw, _ := middleware.NewOTelMetricsMiddleware(next, meter); return mw },
)
```

Use your preferred OpenTelemetry SDK setup for exporters and processors in production; the example uses no-op providers for simplicity.

### Eviction algorithms

Available algorithm names you can pass to `WithEvictionAlgorithm`:

- "lru" — Least Recently Used (default)
- "lfu" — Least Frequently Used (with LRU tie-breaker for equal frequencies)
- "clock" — Second-chance clock
- "cawolfu" — Cache-Aware Write-Optimized LFU
- "arc" — Adaptive Replacement Cache (experimental; not registered by default)

Note: ARC is experimental and isn’t included in the default registry. If you choose to use it, register it manually or enable it explicitly in your build.

## API

The `NewInMemoryWithDefaults` function creates a new `HyperCache` instance with the defaults:

1. The eviction interval is set to 10 minutes.
2. The eviction algorithm is set to LRU.
3. The expiration interval is set to 30 minutes.
4. The capacity of the in-memory backend is set to 1000 items.

To create a new cache with a given capacity, use the New function as described below:

```golang
cache, err := hypercache.NewInMemoryWithDefaults(100)
if err != nil {
    // handle error
}
```

For fine-grained control over the cache configuration, use the `New` function, for instance:

```golang
config := hypercache.NewConfig[backend.InMemory]()
config.HyperCacheOptions = []hypercache.HyperCacheOption[backend.InMemory]{
    hypercache.WithEvictionInterval[backend.InMemory](time.Minute * 10),
    hypercache.WithEvictionAlgorithm[backend.InMemory]("cawolfu"),
}

config.InMemoryOptions = []backend.Option[backend.InMemory]{
    backend.WithCapacity(10),
}

// Create a new HyperCache with a capacity of 10
cache, err := hypercache.New(config)
if err != nil {
    fmt.Fprintln(os.Stderr, err)
    return
}
```

**Refer to the [config.go](./config.go) file for the full configuration options.**
**For a comprehensive API overview, see the [documentation](https://pkg.go.dev/github.com/hyp3rd/hypercache).**

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
