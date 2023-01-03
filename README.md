# HyperCache

[![Go](https://github.com/hyp3rd/hypercache/actions/workflows/go.yml/badge.svg)][def]

## Synopsis

HyperCache is an in-memory cache implementation in Go that supports the background expiration and eviction of items.
It is optimized for performance and flexibility. It uses a read/write lock to synchronize access to the cache, with a custom implementation of a concurrent map, and it allwos the user to specify the expiration and eviction intervals.
It also enables devs to collect stats about the cache with the default [stats collector](./stats/collector.go) or a custom one, and to inject their own eviction algorithm and register it alongisede the default ones:

- [Recently Used (LRU) eviction algorithm](./lru.go)
- [The Adaptive Replacement Cache (ARC) algorithm](./arc.go)
- [The clock eviction algorithm](./clock.go)

### Features

- Store items in the cache with a key and expiration duration
- Retrieve items from the cache by their key
- Delete items from the cache by their key
- Clear the cache of all items
- Evict least recently used items when the cache reaches capacity
- Expire items after a specified duration
- Collect stats

## Installation

Install HyperCache:

```bash
go get github.com/hyp3rd/hypercache
```

## Usage

The `NewHyperCache` function creates a new `HyperCache` struct with the given capacity and initializes the `lru` list and itemsByKey map. It also starts the expiration and eviction loops in separate goroutines.

To create a new cache with a given capacity, use the NewHyperCache function as described below:

```golang
cache, err := hypercache.NewHyperCache(100)
if err != nil {
    // handle error
}
```

### Performance

Running the benchmarks on a 2019 MacBook Pro with a 2.4 GHz 8-Core Intel Core i9 processor and 32 GB 2400 MHz DDR4 memory, the results are as follows:

```bash
go test -bench=. -benchmem -benchtime=4s . -timeout 30m
goos: darwin
goarch: amd64
pkg: github.com/hyp3rd/hypercache/tests/benchmark
cpu: Intel(R) Core(TM) i9-9880H CPU @ 2.30GHz
BenchmarkHyperCache_Get-16      33454436          125.6 ns/op          0 B/op          0 allocs/op
BenchmarkHyperCache_Set-16       4540826          1045 ns/op         156 B/op          4 allocs/op
PASS
ok      github.com/hyp3rd/hypercache/tests/benchmark       10.303s
```

```bash
go test -go test -bench=. -benchmem -benchtime=4s . -timeout 30m
```

Here to follow is described a basic set of functions to get started.

### Set

The `Set` function adds a value to the cache with the given key and duration. It first checks if the key is an empty string, the value is `nil`, or the duration is negative, and returns an error if any of these conditions are true. It then acquires a lock and checks if the item already exists in the cache. If it does, it updates the value and the last accessed time, moves the item to the front of the `lru` list, and releases the lock. If the item doesn't exist, the function checks if the cache is at capacity and evicts the least recently used item if necessary. Finally, it creates a new item and adds it to the front of the `lru` list and the itemsByKey map.

To add an item to the cache, use the Set function as follows:

```golang
err := cache.Set("key", "value", time.Hour)
if err != nil {
    // handle error
}
```

The `Set` function takes a key, a value, and a duration as arguments. The key must be a non-empty string, the value can be of any type, and the duration specifies how long the item should stay in the cache before it expires.

### Get

The `Get` function retrieves an item from the cache with the given key. It first checks if the key is an empty string and returns an error if it is. It then acquires a read lock and looks up the item in the itemsByKey map. If the item exists, it updates the last accessed time, moves it to the front of the `lru` list, and releases the lock. If the item is not found or has expired, it returns an error.

To retrieve an item from the cache, use the `Get` function as follows:

```golang
value, ok := cache.Get("key")
if !ok {
    // handle itme not found
}
```

The `Get` function returns the value associated with the given key or an error if the key is not found or has expired.

### Delete

The `Delete` function removes an item from the cache with the given key. It first checks if the key is an empty string and returns an error if it is. It then acquires a lock, looks up the item in the itemsByKey map, and removes it from the `lru` list and the itemsByKey map. It then releases the lock.

To delete an item from the cache, use the Delete function as follows:

```golang
err := cache.Delete("key")
if err != nil {
    // handle error
}
```

The `Delete` function takes a key as an argument and removes the corresponding item from the cache.

### Clear

The Clear function removes all items from the cache. It acquires a lock, clears the `lru` list and itemsByKey map, and releases the lock.

To clear the cache of all items, use the `Clear` function as follows:

```golang
cache.Clear()
```

### Stop

The `Stop` function stops the expiration and eviction loops. It sends a value to the stop channel.

To stop the expiration and eviction loops, use the `Stop` function as follows:

```golang
cache.Stop()
```

The `Stop` function does not take any arguments and signals the expiration and eviction loops to stop.

## Example

Here is an example of using the HyperCache package:

```golang
package main

import (
    "fmt"
    "time"

    "github.com/hyp3rd/hypercache"
)

func main() {
    // Create a new cache with a capacity of 10 items
    cache, err := hypercache.NewHyperCache(10, hypercache.WithExpirationInterval(10*time.Minute), hypercache.WithEvictionInterval(5*time.Minute))
    if err != nil {
        fmt.Println(err)
        return
    }

    // The method Add allows you to have full control on the item being sent to cache
    onItemExpire := func(key string, value interface{}) {
        f, err := os.Create("my_item.log")
        if err != nil {
            fmt.Println(err)
        }
        // close the file with defer
        defer f.Close()
    }

    cacheItem := hypercache.CacheItem{
        Key:           "NewKey",
        Value:         "hello, there",
        Duration:      2 * time.Minute,
        OnItemExpired: onItemExpire,
    }
    err = cache.Add(cacheItem)
    if err != nil {
        fmt.Println(err)
        return
    }

    // GetOrSet allows you to add an item to cache or fetch it if already present.
    value, ok := cache.GetOrSet("NewKey", "hello, Moon", hypercache.WithDuration(5*time.Minute))
    if ok {
        fmt.Println("Value was retrieved from the cache:", value)
    } else {
        fmt.Println("Value was not found in the cache, so it was set:", value)
    }

    // Add an item to the cache with a key "key" and a value "value" that expires in 5 seconds
    err = cache.Set("key", "value", 5*time.Second)
    if err != nil {
        fmt.Println(err)
        return
    }

    // Retrieve the item from the cache
    value, ok := cache.Get("key")
    if !ok {
        fmt.Println("item not found")
        return
    }
    fmt.Println(value) // "value"

    // Wait for the item to expire
    time.Sleep(5*time.Second)

    // Try to retrieve the expired item from the cache
    _, ok = cache.Get("key")
    if !ok {
        fmt.Println("item not found")
        return
    }

    // Stop the expiration and eviction loops
    cache.Stop()
}
```

## License

The code and documentation in this project are released under the Mozilla Public License 2.0.

## Author

I'm a surfer, a crypto trader, and a software architect with 15 years of experience designing highly available distributed production environments and developing cloud-native apps in public and private clouds. Feel free to hook me up on [LinkedIn](https://www.linkedin.com/in/francesco-cosentino/).

[def]: https://github.com/hyp3rd/hypercache/actions/workflows/go.yml
