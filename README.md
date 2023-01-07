# HyperCache

[![Go](https://github.com/hyp3rd/hypercache/actions/workflows/go.yml/badge.svg)][build-link] [![CodeQL](https://github.com/hyp3rd/hypercache/actions/workflows/codeql.yml/badge.svg)][codeql-link]

## Synopsis

HyperCache is a **thread-safe** and **high-performance** in-memory cache implementation in Go that supports items' background expiration and eviction.
It is optimized for performance and flexibility. It uses a read/write lock to synchronize access to the cache with a custom implementation of a concurrent map. It also allows the user to specify the expiration and eviction intervals.
It also enables devs to collect stats about the cache with the default [stats collector](./stats/collector.go) or a custom one and to inject their own eviction algorithm and register it alongside the default ones:

- [Recently Used (LRU) eviction algorithm](./lru.go)
- [The Adaptive Replacement Cache (ARC) algorithm](./arc.go)
- [The clock eviction algorithm](./clock.go)
- [The Least Frequently Used (LFU) algorithm](./lfu.go)
- [Cache-Aware Write-Optimized LFU (CAWOLFU)](./cawolfu.go)

### Features

- Store items in the cache with a key and expiration duration
- Retrieve items from the cache by their key
- Delete items from the cache by their key
- Clear the cache of all items
- Evitc items in the background based on the cache capacity and items access leveraging several custom eviction algorithms
- Expire items in the background based on their duration
- [EvictionAlgorithm interface](./eviction.go) to implement custom eviction algorithms.
- Stats collection with a default [stats collector](./stats/collector.go) or a custom one that implements the StatsCollector interface.

## Installation

Install HyperCache:

```bash
go get github.com/hyp3rd/hypercache
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
BenchmarkHyperCache_Get-16                          38833602           123.9 ns/op         0 B/op          0 allocs/op
BenchmarkHyperCache_Get_ProactiveEviction-16        38079158           124.4 ns/op         0 B/op          0 allocs/op
BenchmarkHyperCache_Set-16                           4361000          1217 ns/op         203 B/op          3 allocs/op
BenchmarkHyperCache_Set_Proactive_Eviction-16        4343996          1128 ns/op          92 B/op          3 allocs/op
PASS
ok      github.com/hyp3rd/hypercache/tests/benchmark    23.723s
```

### Examples

To run the examples, use the following command:

```bash
make run example=eviction  # or any other example
```

For a full list of examples, refer to the [examples](./examples/README.md) directory.

## API

The `NewHyperCache` function creates a new `HyperCache` instance with the given capacity and initializes the `eviction` algorithm, applying any other configuration [option](./options.go). It also starts the expiration and eviction loops in separate goroutines.

To create a new cache with a given capacity, use the NewHyperCache function as described below:

```golang
cache, err := hypercache.NewHyperCache(100)
if err != nil {
    // handle error
}
```

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

## Usage

Here is an example of using the HyperCache package. For a more comprehensive overview, see the [examples](./examples/README.md) directory.

```golang
package main

import (
    "fmt"
    "time"

    "github.com/hyp3rd/hypercache"
)

func main() {
    // create a new cache with a capacity of 100 items
    cache := hypercache.NewHyperCache(100)
    defer cache.Stop()

    // set a key-value pair in the cache with an expiration duration of 1 minute
    cache.Set("key", "value", time.Minute)

    // get the value for the key from the cache
    val, err := cache.Get("key")
    if err != nil {
        fmt.Println(err)
    } else {
        fmt.Println(val) // "value"
    }

    // wait for the item to expire
    time.Sleep(time.Minute)

    // try to get the value for the key again
    val, err = cache.Get("key")
    if err != nil {
        fmt.Println(err) // "key not found"
    }
}
```

## License

The code and documentation in this project are released under Mozilla Public License 2.0.

## Author

I'm a surfer, a crypto trader, and a software architect with 15 years of experience designing highly available distributed production environments and developing cloud-native apps in public and private clouds. Feel free to hook me up on [LinkedIn](https://www.linkedin.com/in/francesco-cosentino/).

[build-link]: https://github.com/hyp3rd/hypercache/actions/workflows/go.yml
[codeql-link]:https://github.com/hyp3rd/hypercache/actions/workflows/codeql.yml
