---
title: Quickstart
---

# Quickstart

Five minutes from `go get` to a working cache. Two paths: embed the
library in a Go program, or run the binary and talk to it over HTTP.

## Library (single process, no cluster)

```sh
go get github.com/hyp3rd/hypercache@latest
```

```go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/hyp3rd/hypercache"
)

func main() {
	ctx := context.Background()

	cache, err := hypercache.NewInMemoryWithDefaults(ctx, 10_000)
	if err != nil {
		panic(err)
	}

	defer cache.Stop(ctx)

	if err := cache.Set(ctx, "greeting", "hello", 5*time.Minute); err != nil {
		panic(err)
	}

	v, ok := cache.Get(ctx, "greeting")
	fmt.Printf("got: %v ok=%v\n", v, ok)
}
```

That's the full library surface. Capacity, eviction algorithm, expiration
interval, and per-shard tuning are all configurable via `hypercache.Config`
+ `WithDist*` / `With*` options.

## Service (single node, HTTP API)

Run the binary directly:

```sh
go install github.com/hyp3rd/hypercache/cmd/hypercache-server@latest

HYPERCACHE_NODE_ID=demo \
HYPERCACHE_API_ADDR=:8080 \
HYPERCACHE_DIST_ADDR=127.0.0.1:7946 \
hypercache-server
```

In another terminal:

```sh
# Store a value.
curl -X PUT --data 'world' 'http://localhost:8080/v1/cache/greeting'

# Read it back.
curl 'http://localhost:8080/v1/cache/greeting'           # -> world

# Inspect metadata via headers (no body transfer).
curl -I 'http://localhost:8080/v1/cache/greeting'

# Or as a JSON envelope.
curl -H 'Accept: application/json' 'http://localhost:8080/v1/cache/greeting'

# Batch operations (3 endpoints under /v1/cache/batch/{get,put,delete}).
curl -X POST -H 'Content-Type: application/json' \
     --data '{"keys": ["greeting", "missing"]}' \
     'http://localhost:8080/v1/cache/batch/get'
```

The binary's full env-var reference and the response shapes are documented
on the [Server Binary](server.md) page.

## Service (5-node cluster on docker-compose)

For a real cluster, see the [5-Node Cluster](cluster.md) tutorial — one
command brings five nodes up on a docker network, replication factor 3,
quorum reads/writes, with the same client API.

## Production deployment

[Kubernetes via Helm](helm.md) is the canonical production deployment.
The chart wires up StatefulSet identities, headless DNS for peer
discovery, anti-affinity, PodDisruptionBudget, and an optional
operator-managed Secret for the bearer token.
