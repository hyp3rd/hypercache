# HyperCache

[![Go](https://github.com/hyp3rd/hypercache/actions/workflows/go.yml/badge.svg)][def]

## Synopsis

HyperCache is an in-memory cache implementation in Go that supports the expiration and eviction of items. The package consists of a struct called **HyperCache**, which represents the cache itself and several functions that operate on it.

### Features

- Store items in the cache with a key and expiration duration
- Retrieve items from the cache by their key
- Delete items from the cache by their key
- Clear the cache of all items
- Evict least recently used items when the cache reaches capacity
- Expire items after a specified duration

## Installation

Install HyperCache using go get:

```bash
go get github.com/hyp3rd/hypercache
```

## Usage

The `NewHyperCache` function creates a new `HyperCache` struct with the given capacity and initializes the `lru` list and itemsByKey map. It also starts the expiration and eviction loops in separate goroutines.

To create a new cache with a given capacity, use the NewHyperCache function as follows:

```golang
cache, err := hypercache.NewHyperCache(100)
if err != nil {
    // handle error
}
```

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
    // Create a new cache with a capacity of 100 items
    cache, err := hypercache.NewHyperCache(100)
    if err != nil {
        fmt.Println(err)
        return
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
