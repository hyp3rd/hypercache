# HyperCache

## Synopsis

HyperCache is an in-memory cache implementation in Go that supports the expiration and eviction of items.
The package consists of a struct called HyperCache, which represents the cache itself and several functions that operate on it.

The HyperCache struct has the following fields:

`mu`: a `sync.RWMutex` to protect concurrent access to the cache;
`lru`: a doubly-linked list of `*item objects` implemented leveraging the `list` package from the standard Golang library to store the items in the cache;
`itemsByKey`: a map of keys to list elements used to quickly lookup items by their key;
`capacity`: an integer field that limits the number of items allowed in the cache;
`stop`: a channel of bool values used to signal the expiration and eviction loops to stop;
The `item` struct represents a single item in the cache and has the following fields:

`key`: a `string` representing the key of the item;
`value`: an `interface{}` value representing the value of the item;
`lastAccessedBefore`: a time.Time value representing the last time the item was accessed;
`duration`: a `time.Duration` value representing the duration for which the item should stay in the cache before it expires;
The `itemList` type is a wrapper around a slice of pointers to item objects and implements the sort.Interface interface, which allows it to be sorted.

## Installation

Install HyperCache using `go get`:

```bash
go get github.com/user/hypercache
```

## Usage

The `NewHyperCach` function creates a new HyperCache struct with the given capacity and initializes the `lru` list and `itemsByKey` map. It also starts the expiration and eviction loops in separate goroutines.

To create a new cache with a given capacity, use the NewHyperCache function as follows:

```golang
cache, err := hypercache.NewHyperCache(100)
if err != nil {
    // handle error
}
```

### Set

The `Set` function adds a value to the cache with the given key and duration. It first checks if the key is an empty string, the value is `nil`, or the duration is negative, and returns an error if any of these conditions are true. It then acquires a lock and checks if the item already exists in the cache. If it does, it updates the value and the last accessed time, moves the item to the front of the `lru` list, and releases the lock. If the item doesn't exist, the function checks if the cache is at capacity and evicts the least recently used item if necessary. Finally, it creates a new item and adds it to the front of the `lru` list and the `itemsByKey` map.

To add an item to the cache, use the `Set` function as follows:

```golang
err := cache.Set("key", "value", time.Hour)
if err != nil {
    // handle error
}
```

The `Set` function takes a key, a value, and a duration as arguments. The key must be a non-empty string, the value can be of any type, and the duration specifies how long the item should stay in the cache before it expires.

### Get

The `Get` function retrieves an item from the cache with the given key. It first checks if the key is an empty string and returns an error if it is. It then acquires a read lock and looks up the item in the `itemsByKey` map. If the item exists, it updates the last accessed time, moves it to the front of the `lru` list, and releases the lock. If the item is not found or has expired, it returns an error.

To retrieve an item from the cache, use the `Get` function as follows:

```golang
value, ok := cache.Get("key")
if !ok {
    // handle itme not found
}
```

The Get function returns the value associated with the given key or an error if the key is not found or has expired.

### Delete

The `Delete` function removes an item from the cache with the given key. It first checks if the key is an empty string and returns an error if it is. It then acquires a lock, looks up the item in the `itemsByKey` map, and removes it from the `lru` list and the `itemsByKey` map. It then releases the lock.

To remove an item from the cache, use the `Delete` function as follows:

```golang
err := cache.Delete("key")
if err != nil {
    // handle error
}
```

### Cache Eviction

If the number of items in the cache exceeds the capacity, the cache will automatically evict the least recently used items to make room for new ones.
The `evictionLoop` function also runs in a separate goroutine. It removes the least recently used items from the cache until the number of items in the cache returns within the boundaries of the capacity. It first sorts the `lru` list by the last accessed time of the items, with the least recently used items. It then iterates over the sorted list and removes items until it restores the capacity.

### Cache Expiration

Items in the cache can also expire based on their specified duration
The expiration relies on the `expirationLoop`, a function running in the background with a separate goroutine that removes expired items from the cache. It first sorts the `lru` list by the last accessed time of the items, with the least recently used items. It then iterates over the sorted list and removes any expired items.

### Clean

The `Clean` function removes all expired items from the cache.

### Stop

The `Stop` function sends a signal to the expiration and eviction loops to stop.
To stop the expiration and eviction loops, use the Stop function as follows:

```golang
cache.Stop()
```

## Example

Here is an example of how to use the HyperCache package:

```golang
package main

import (
    "fmt"
    "time"

    "github.com/hyp3rd/hypercache"
)

func main() {
    cache, err := hypercache.NewHyperCache(100)
    if err != nil {
        fmt.Println(err)
        return
    }
    defer cache.Stop()

    // Add an item to the cache
    err = cache.Set("key", "value", 5*time.Second)
    if err != nil {
        fmt.Println(err)
        return
    }

    // Retrieve the item from the cache
    value, ok := cache.Get("key")
    if !ok {
        fmt.Println("unable to retrieve the item from cache")
        return
    }
    fmt.Println(value)

    // Wait for the item to expire
    time.Sleep(5*time.Second)

    // Try to retrieve the item again
    value, ok = cache.Get("key")
    if !ok {
        fmt.Println("the item you're trying to retrieve expired")
        return
    }
    fmt.Println(value)

    // Remove the item from the cache
    err = cache.Delete("key")
    if err != nil {
        fmt.Println(err)
        return
    }
}
```

The example creates a new cache with a capacity of 100, adds an item with a key of "key" and a value of "value", and sets the expiration duration to 1 hour. It then retrieves the item from the cache and prints its value. After waiting for 1 hour, it tries to retrieve the item again, but it has already expired and is no longer in the cache. Finally, it removes the item from the cache.

## License

The code and documentation in this project are released under the Mozilla Public License 2.0.

## Author

I'm a surfer, a crypto trader, and a software architect with 15 years of experience designing highly available distributed production environments and developing cloud-native apps in public and private clouds. Feel free to hook me up on [LinkedIn](https://www.linkedin.com/in/francesco-cosentino/).
