# hypercache-server

Production binary that runs a single HyperCache node configured for
the distributed in-memory backend (DistMemory). One process per node;
multiple processes form a cluster via the dist HTTP transport.

## Listeners

The binary exposes three independent HTTP listeners on every node:

| Listener | Default | Purpose |
|---|---|---|
| Client API | `:8080` | Apps `PUT`/`GET`/`DELETE` keys here |
| Management | `:8081` | Admin + observability (`/health`, `/stats`, `/config`, `/dist/metrics`, `/cluster/*`) |
| Dist | `:7946` | Peer-to-peer replication, anti-entropy, heartbeat |

Override any of them via `HYPERCACHE_API_ADDR`, `HYPERCACHE_MGMT_ADDR`,
`HYPERCACHE_DIST_ADDR` (each a `host:port` or `:port` string).

## Configuration

All configuration is via environment variables — 12-factor style, so
the same binary runs unchanged in Docker, k8s, and bare-metal.

| Variable | Default | Description |
|---|---|---|
| `HYPERCACHE_NODE_ID` | `<hostname>` | Stable identifier for this node within the cluster |
| `HYPERCACHE_API_ADDR` | `:8080` | Bind address for the client REST API |
| `HYPERCACHE_MGMT_ADDR` | `:8081` | Bind address for the management HTTP server |
| `HYPERCACHE_DIST_ADDR` | `:7946` | Bind + advertise address for peer-to-peer dist HTTP |
| `HYPERCACHE_SEEDS` | _(empty)_ | Comma-separated peer addresses to bootstrap membership |
| `HYPERCACHE_REPLICATION` | `3` | Replicas per key |
| `HYPERCACHE_CAPACITY` | `100000` | Total cache capacity (items) |
| `HYPERCACHE_AUTH_TOKEN` | _(empty)_ | Bearer token applied to client API + dist HTTP + management HTTP |
| `HYPERCACHE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error` |
| `HYPERCACHE_HEARTBEAT` | `1s` | Heartbeat probe interval |
| `HYPERCACHE_INDIRECT_PROBE_K` | `2` | SWIM indirect-probe relay count (0 disables) |
| `HYPERCACHE_HINT_TTL` | `30s` | How long failed forwards stay queued for retry |
| `HYPERCACHE_HINT_REPLAY` | `200ms` | Hint replay loop tick |
| `HYPERCACHE_REBALANCE_INTERVAL` | `250ms` | Ownership-rebalance scan interval |

## Client API

Bearer auth is applied to all routes when `HYPERCACHE_AUTH_TOKEN` is
set; without a token the API is open. Add an
`Authorization: Bearer TOKEN` header to every request when auth is on.

```sh
# Set a key (raw bytes, optional ttl).
curl -H 'Authorization: Bearer dev-token' \
     -X PUT --data 'hello' \
     'http://localhost:8080/v1/cache/greeting?ttl=5m'

# Fetch it back.
curl -H 'Authorization: Bearer dev-token' \
     'http://localhost:8080/v1/cache/greeting'

# Delete it.
curl -H 'Authorization: Bearer dev-token' \
     -X DELETE \
     'http://localhost:8080/v1/cache/greeting'

# Liveness probe (no auth).
curl 'http://localhost:8080/healthz'
```

Bodies are treated as opaque bytes; `Content-Type` round-trips as
`application/octet-stream`. Strings round-trip cleanly; structured
values are JSON-encoded on response.

### Metadata inspection

`HEAD` returns the value's metadata in `X-Cache-*` response headers
(no body — fast existence + TTL check):

```sh
curl -I -H 'Authorization: Bearer dev-token' \
     'http://localhost:8080/v1/cache/greeting'
# X-Cache-Version: 1
# X-Cache-Origin: node-1
# X-Cache-Last-Updated: 2026-05-06T10:00:00Z
# X-Cache-Ttl-Ms: 28412
# X-Cache-Expires-At: 2026-05-06T10:30:00Z
# X-Cache-Owners: node-1,node-2,node-3
# X-Cache-Node: node-1
```

`GET` with `Accept: application/json` returns the same metadata as
a JSON envelope (value is base64 for binary fidelity):

```sh
curl -H 'Authorization: Bearer dev-token' \
     -H 'Accept: application/json' \
     'http://localhost:8080/v1/cache/greeting'
# {
#   "key": "greeting",
#   "value": "d29ybGQ=",
#   "value_encoding": "base64",
#   "ttl_ms": 28412,
#   "expires_at": "2026-05-06T10:30:00Z",
#   "version": 1,
#   "origin": "node-1",
#   "last_updated": "2026-05-06T10:00:00Z",
#   "node": "node-1",
#   "owners": ["node-1", "node-2", "node-3"]
# }
```

### Batch operations

Three endpoints over `POST /v1/cache/batch/{get,put,delete}`. Each
returns a `results` array with one entry per requested item; per-item
errors are surfaced without failing the whole batch.

```sh
# Batch put — mixed UTF-8 strings and base64-encoded byte payloads.
curl -H 'Authorization: Bearer dev-token' \
     -X POST -H 'Content-Type: application/json' \
     --data '{
       "items": [
         {"key": "greet-en", "value": "hello", "ttl_ms": 60000},
         {"key": "greet-bin", "value": "d29ybGQ=", "value_encoding": "base64"}
       ]
     }' \
     'http://localhost:8080/v1/cache/batch/put'

# Batch get — fetches many keys in one round-trip; results carry
# the same envelope shape as the single-key Accept:json GET.
curl -H 'Authorization: Bearer dev-token' \
     -X POST -H 'Content-Type: application/json' \
     --data '{"keys": ["greet-en", "greet-bin", "missing"]}' \
     'http://localhost:8080/v1/cache/batch/get'

# Batch delete.
curl -H 'Authorization: Bearer dev-token' \
     -X POST -H 'Content-Type: application/json' \
     --data '{"keys": ["greet-en", "greet-bin"]}' \
     'http://localhost:8080/v1/cache/batch/delete'
```

Default `value_encoding` for batch-put items is the literal UTF-8
string. Pass `"value_encoding": "base64"` for binary payloads.

## Graceful shutdown

On `SIGTERM` / `SIGINT` the binary runs:

1. Drain dist (`/health` returns 503, writes return `ErrDraining`).
1. Shut down the client API listener (in-flight requests get up to
   30 s to finish).
1. Stop the cache (which also stops the management HTTP and the dist
   HTTP listeners).

This sequence lets external load balancers stop routing traffic
before any in-flight write fails. Drain → Stop is one-way; restart
the process to clear it.

## Local 5-node cluster

A ready-to-run Compose file lives at the repo root:

```sh
docker compose -f docker-compose.cluster.yml up --build
```

Five nodes join via the shared `hypercache-cluster` network. Client
APIs are exposed on host ports `8081..8085`; management HTTP on
`9081..9085`. See [docker-compose.cluster.yml](../../docker-compose.cluster.yml)
for the per-node port map.

End-to-end smoke test (from another terminal):

```sh
# Write to node-1.
curl -H 'Authorization: Bearer dev-token' \
     -X PUT --data 'world' \
     'http://localhost:8081/v1/cache/hello'

# Read from node-5 — same value, served by whichever owner the ring
# routed to.
curl -H 'Authorization: Bearer dev-token' \
     'http://localhost:8085/v1/cache/hello'

# Inspect cluster membership from node-2.
curl -H 'Authorization: Bearer dev-token' \
     'http://localhost:9082/cluster/members'
```

## Operational notes

See [`docs/operations.md`](../../docs/operations.md) for the full
runbook covering split-brain, hint-queue overflow, rebalance under
load, and replica loss. Each failure mode maps to specific metrics
exposed by this binary's management HTTP server and OpenTelemetry
pipeline.
