---
title: 5-Node Cluster
---

# 5-Node Cluster (docker-compose)

The repo ships a ready-to-run 5-node cluster definition at
[`docker-compose.cluster.yml`](https://github.com/hyp3rd/hypercache/blob/main/docker-compose.cluster.yml).
Replication factor 3, quorum reads/writes, bearer-token auth, peer-to-peer
DNS via container hostnames.

## Bring it up

```sh
docker compose -f docker-compose.cluster.yml up --build -d
```

Wait for the listeners to bind:

```sh
bash scripts/tests/wait-for-cluster.sh
```

## Talk to it

| Node | Client API host port | Management host port |
|---|---|---|
| `hypercache-1` | `8081` | `9081` |
| `hypercache-2` | `8082` | `9082` |
| `hypercache-3` | `8083` | `9083` |
| `hypercache-4` | `8084` | `9084` |
| `hypercache-5` | `8085` | `9085` |

Every node accepts every operation — the dist backend's quorum and
forwarding logic routes to the actual key owners under the hood:

```sh
TOKEN='dev-token'

# Write to node-1.
curl -H "Authorization: Bearer $TOKEN" \
     -X PUT --data 'world' \
     'http://localhost:8081/v1/cache/greeting'

# Read from any other node — same value.
curl -H "Authorization: Bearer $TOKEN" \
     'http://localhost:8085/v1/cache/greeting'        # -> world

# See which ring nodes own the key.
curl -H "Authorization: Bearer $TOKEN" \
     'http://localhost:8083/v1/owners/greeting'
```

## Inspect cluster state

The management HTTP server (host ports `9081-9085`) exposes the admin
endpoints:

```sh
curl -H "Authorization: Bearer $TOKEN" 'http://localhost:9081/cluster/members'
curl -H "Authorization: Bearer $TOKEN" 'http://localhost:9081/dist/metrics' | jq
curl -H "Authorization: Bearer $TOKEN" 'http://localhost:9081/cluster/heartbeat'
```

## Verify with the regression scripts

Two scripts under `scripts/tests/` assert end-to-end behavior — a smoke
covering propagation/wire-encoding/cross-node delete, and a resilience
test that kills a node mid-run and asserts the cluster keeps serving:

```sh
bash scripts/tests/10-test-cluster-api.sh        # 17 assertions
bash scripts/tests/20-test-cluster-resilience.sh # 24 assertions, ~20s
```

Or chain everything via the Makefile:

```sh
make test-cluster   # up + smoke + resilience + always-down
```

## What changed when

The journey from "this didn't actually cluster" to a tested 5-node stack
is documented in the [changelog](changelog.md). Notable fixes:

- Factory was discarding `cfg.DistMemoryOptions` — every `WithDistNode` /
  `WithDistSeeds` was a silent no-op until [v0.6.0].
- Seeds without inline node IDs produced an unusable ring; the
  `id@addr` syntax (`node-2@hypercache-2:7946`) is the production form.
- Cross-process gossip dissemination + SWIM self-refutation
  ([v0.6.0]) retired the `experimental` heartbeat marker.

[v0.6.0]: changelog.md
