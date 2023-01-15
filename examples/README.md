# HyperCache Examples

This directory contains examples of using the HyperCache package.
**Do not use these examples in production.**
All the code in this directory is for demonstration purposes only.

1. [`Get`](./get/get.go) - An example of using the HyperCache package to fetch one or more items and retrieve a single or multiple items from cache.

2. [`List`](./list/list.go) - A simple example of using the HyperCache package to store a list of items and retrieve the list filtering and sorting the results.

3. [`Eviction`](./eviction/eviction.go) - An example of using the HyperCache package to store a list of items and evict items from the cache based on several different algorithms.

4. [`Stats`](./stats/stats.go) - An example of using the HyperCache package to store a list of items and retrieve the cache stats.

5. [`Clear`](./clear/clear.go) - An example of using the HyperCache package to store a list of items and clear the cache.

6. [`Service`](./service/service.go) - An example of implementing `HyperCacheService` and register middleware.
