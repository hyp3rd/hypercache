# HyperCache

[![Go](https://github.com/hyp3rd/hypercache/actions/workflows/go.yml/badge.svg)][build-link] [![CodeQL](https://github.com/hyp3rd/hypercache/actions/workflows/codeql.yml/badge.svg)][codeql-link]

## Synopsis

HyperCache is a **thread-safe** **high-performance** cache implementation in `Go` that supports multiple backends with optional size limit, expiration and eviction of items supporting custom algorithms alongside the defaults. It can be used as a standalone cache or as a cache middleware for a service. It can implement a [service interface](./service.go) to intercept and decorate the cache methods with middleware (default or custom).
It is optimized for performance and flexibility allowing to specify the expiration and eviction intervals, provide and register new eviction algorithms, stats collectors, middleware(s).
It ships with a default [historigram stats collector](./stats/statscollector.go) and several eviction algorithms, but you can develop and register your own as long as it implements the [Eviction Algorithm interface](./eviction/eviction.go).:

- [Recently Used (LRU) eviction algorithm](./eviction/lru.go)
- [The Least Frequently Used (LFU) algorithm](./eviction/lfu.go)
- [Cache-Aware Write-Optimized LFU (CAWOLFU)](./eviction/cawolfu.go)
- [The Adaptive Replacement Cache (ARC) algorithm](./eviction/arc.go)
- [The clock eviction algorithm](./eviction/clock.go)

### Features

- Thread-safe
- High-performance
- Supports multiple backends, default backends are:
    1. [In-memory](./backend/inmemory.go)
    2. [Redis](./backend/redis.go)
- Store items in the cache with a key and expiration duration
- Retrieve items from the cache by their key
- Delete items from the cache by their key
- Clear the cache of all items
- Evitc items in the background based on the cache capacity and items access leveraging several custom eviction algorithms
- Expire items in the background based on their duration
- [Eviction Algorithm interface](./eviction.go) to implement custom eviction algorithms.
- Stats collection with a default [stats collector](./stats/statscollector.go) or a custom one that implements the StatsCollector interface.
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
BenchmarkHyperCache_Get-16                          39429110           115.7 ns/op         0 B/op          0 allocs/op
BenchmarkHyperCache_Get_ProactiveEviction-16        42094736           118.0 ns/op         0 B/op          0 allocs/op
BenchmarkHyperCache_List-16                         10898176           437.0 ns/op        85 B/op          1 allocs/op
BenchmarkHyperCache_Set-16                           3034786          1546 ns/op         252 B/op          4 allocs/op
BenchmarkHyperCache_Set_Proactive_Eviction-16        2725557          1833 ns/op         162 B/op          3 allocs/op
PASS
ok      github.com/hyp3rd/hypercache/tests/benchmark    30.031s
```

### Examples

To run the examples, use the following command:

```bash
make run example=eviction  # or any other example
```

For a full list of examples, refer to the [examples](./examples/README.md) directory.

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

For a fine grained control over the cache configuration, use the `New` function, for instance:

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
    fmt.Println(err)
    return
}
```

**For the full set of configuration options, refer to the [config.go](./config.go) file.**

### Set

Set adds an item to the cache with the given key and value.

```golang
err := cache.Set("key", "value", time.Hour)
if err != nil {
    // handle error
}
```

The `Set` function takes a key, a value, and a duration as arguments. The key must be a non-empty string, the value can be of any type, and the duration specifies how long the item should stay in the cache before it expires.

### Get

`Get` retrieves the item with the given key from the cache.

```golang
value, ok := cache.Get("key")
if !ok {
    // handle item not found
}
```

The `Get` function returns the value associated with the given key or an error if the key is not found or has expired.

### Remove

`Remove` deletes items with the given key from the cache. If an item is not found, it does nothing.

```golang
err := cache.Remove("key", "key2", "key3")
if err != nil {
    // handle error
}
```

The `Remove` function takes a variadic number of keys as arguments and returns an error if any keys are not found.

**For a comprehensive API overview, see the [documentation](https://pkg.go.dev/github.com/hyp3rd/hypercache).**

## Service interface for microservices implementation

The `Service` interface allows intercepting cache methods and decorate them with custom or default middleware(s).

```golang
var svc hypercache.Service
hyperCache, err := hypercache.NewInMemoryWithDefaults(10)

if err != nil {
    fmt.Println(err)
    return
}
// assign statsCollector of the backend to use it in middleware
statsCollector := hyperCache.StatsCollector
svc = hyperCache

if err != nil {
    fmt.Println(err)
    return
}

// Example of using zap logger from uber
logger, _ := zap.NewProduction()

sugar := logger.Sugar()
defer sugar.Sync()
defer logger.Sync()

// apply middleware in the same order as you want to execute them
svc = hypercache.ApplyMiddleware(svc,
    // middleware.YourMiddleware,
    func(next hypercache.Service) hypercache.Service {
        return middleware.NewLoggingMiddleware(next, sugar)
    },
    func(next hypercache.Service) hypercache.Service {
        return middleware.NewStatsCollectorMiddleware(next, statsCollector)
    },
)

err = svc.Set("key string", "value any", 0)
if err != nil {
    fmt.Println(err)
    return
}
key, ok := svc.Get("key string")
if !ok {
    fmt.Println("key not found")
    return
}
fmt.Println(key)
```

## Usage

Here is an example of using the HyperCache package. For a more comprehensive overview, see the [examples](./examples/README.md) directory.

```golang
package main

import (
    "fmt"
    "log"
    "time"

    "github.com/hyp3rd/hypercache"
)

func main() {
    // Create a new HyperCache with a capacity of 10
    cache, err := hypercache.NewInMemoryWithDefaults(10)
    if err != nil {
        fmt.Println(err)
        return
    }
    // Stop the cache when the program exits
    defer cache.Stop()

    log.Println("adding items to the cache")
    // Add 10 items to the cache
    for i := 0; i < 10; i++ {
        key := fmt.Sprintf("key%d", i)
        val := fmt.Sprintf("val%d", i)

        err = cache.Set(key, val, time.Minute)

        if err != nil {
            fmt.Printf("unexpected error: %v\n", err)
            return
        }
    }

    log.Println("fetching items from the cache using the `GetMultiple` method, key11 does not exist")
    // Retrieve the specific of items from the cache
    items, errs := cache.GetMultiple("key1", "key7", "key9", "key11")

    // Print the errors if any
    for k, e := range errs {
        log.Printf("error fetching item %s: %s\n", k, e)
    }

    // Print the items
    for k, v := range items {
        fmt.Println(k, v)
    }

    log.Println("fetching items from the cache using the `GetOrSet` method")
    // Retrieve a specific of item from the cache
    // If the item is not found, set it and return the value
    val, err := cache.GetOrSet("key11", "val11", time.Minute)
    if err != nil {
        fmt.Println(err)
        return
    }
    fmt.Println(val)

    log.Println("fetching items from the cache using the simple `Get` method")
    item, ok := cache.Get("key7")
    if !ok {
        fmt.Println("item not found")
        return
    }
    fmt.Println(item)
}

```

## License

The code and documentation in this project are released under Mozilla Public License 2.0.

## Author

I'm a surfer, a crypto trader, and a software architect with 15 years of experience designing highly available distributed production environments and developing cloud-native apps in public and private clouds. Feel free to hook me up on [LinkedIn](https://www.linkedin.com/in/francesco-cosentino/).

[build-link]: https://github.com/hyp3rd/hypercache/actions/workflows/go.yml
[codeql-link]:https://github.com/hyp3rd/hypercache/actions/workflows/codeql.yml
