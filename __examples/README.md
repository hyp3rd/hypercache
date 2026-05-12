# HyperCache Examples

This directory contains examples of using the HyperCache package.
**Do not use these examples in production.**
All the code in this directory is for demonstration purposes only.

1. [`Get`](./get/get.go) - An example of using the HyperCache package to fetch one or more items and retrieve a single or multiple items from cache.

1. [`List`](./list/list.go) - A simple example of using the HyperCache package to store a list of items and retrieve the list filtering and sorting the results.

1. [`Eviction`](./eviction/eviction.go) - An example of using the HyperCache package to store a list of items and evict items from the cache based on several different algorithms.

1. [`Stats`](./stats/stats.go) - An example of using the HyperCache package to store a list of items and retrieve the cache stats.

1. [`Clear`](./clear/clear.go) - An example of using the HyperCache package to store a list of items and clear the cache.

1. [`Service`](./service/service.go) - An example of implementing `HyperCacheService` and register middleware.

1. [`Redis`](./redis/redis.go) - An example of implementing the `HyperCache` interface using Redis as the backend. It requires that you run the Redis server locally as the default configuration points to `localhost:6379`. To run the Redis server locally, use the following command: `docker compose up -d`

1. [`Middleware`](./middleware/middleware.go) - An example of implementing a custom middleware and register it with the `HyperCacheService`.

1. [`Size`](./size/size.go) - An example of using the HyperCache package to store a list of items and limit the cache based on size.

1. [`Observability (OpenTelemetry)`](./observability/otel.go) - Demonstrates wrapping the service with tracing and metrics middleware using OpenTelemetry.

1. [`Distributed OIDC client (SDK)`](./distributed-oidc-client/) - **Recommended**: ~150-line consumer using [`pkg/client`](../pkg/client/) for OIDC client-credentials auth, multi-endpoint failover, topology refresh, and typed errors. The path most Go integrators should follow. See [`docs/client-sdk.md`](../docs/client-sdk.md) for the full SDK reference.

1. [`Distributed OIDC client (raw HTTP)`](./distributed-oidc-client-raw/) - The hand-crafted version of the above against `net/http` — kept in the tree as a reference for what the SDK does internally and for environments that can't depend on `pkg/client` (non-Go consumers reading along, code-review reference, etc.).
