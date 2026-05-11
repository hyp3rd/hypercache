# Operations runbook — DistMemory

This document is for operators running the `pkg/backend.DistMemory` distributed backend in production. It
assumes the design background in [distributed.md](distributed.md). Sections are deliberately short — each one
stands on its own and links to code.

<!-- prettier-ignore -->
!!! tip "Paged right now?"
    Start at the [on-call cheatsheet](oncall.md). It maps a symptom (heartbeat flap, hint queue building,
    auth failure, drain stuck) to the exact log lines and metrics to grep for, then back-links to the
    relevant section here.

## At a glance

| Concern                    | First place to look                                                              |
| -------------------------- | -------------------------------------------------------------------------------- |
| Node not receiving traffic | `dist.members.alive`, `/health`                                                  |
| Writes failing             | `dist.write.quorum_failures`, `sentinel.ErrDraining`, `sentinel.ErrQuorumFailed` |
| Replicas falling behind    | `dist.hinted.queued`, `dist.hinted.replayed`, `dist.hinted.dropped`              |
| Bandwidth pressure         | `DistHTTPLimits.CompressionThreshold`                                            |
| Spurious peer flapping     | `dist.heartbeat.indirect_probe.refuted`, `WithDistIndirectProbes`                |
| Slow rebalance             | `dist.rebalance.throttle`, `dist.rebalance.last_ns`                              |
| Anti-entropy backlog       | `dist.merkle.last_diff_ns`, `dist.auto_sync.last_ns`                             |

Live metric values come from `DistMemory.Metrics()` (Go struct), `/dist/metrics` (JSON, when wrapped in
`hypercache.HyperCache`), or the OpenTelemetry pipeline you wired via `WithDistMeterProvider`. The OTel names
use the `dist.` prefix.

## Wiring observability

Three opt-in entry points, all defaulting to no-op:

- **Logging** — `backend.WithDistLogger(*slog.Logger)` routes background loops (heartbeat, hint replay,
  rebalance, merkle sync) and operational errors into your logger. Records are pre-bound with
  `component=dist_memory` and `node_id=<id>`.
- **Tracing** — `backend.WithDistTracerProvider(trace.TracerProvider)` opens spans on `Get`/`Set`/`Remove`
  plus per-peer `dist.replicate.*` child spans. Cache key _values_ are never put on spans (they can be PII);
  only `cache.key.length`.
- **Metrics** — `backend.WithDistMeterProvider(metric.MeterProvider)` exposes every field on `DistMetrics` as
  an observable instrument.

Wire all three to the same `otel.SetTracerProvider` / `otel.SetMeterProvider` your application uses; the
logger inherits via `slog.Default()` if you want a one-liner.

## Failure mode — split-brain

**Symptom.** Two subsets of the cluster lose connectivity to each other. Each subset elects local primaries
for the keys it owns. Writes from clients on subset A land on A-side primaries; writes from B-side clients
land on B-side primaries. When the partition heals, the versions diverge.

**Detection.** `dist.heartbeat.failure` rises on both sides during the partition. After healing,
`dist.version.conflicts` increments as anti-entropy reconciles.

**Resolution.** DistMemory uses last-write-wins by `(version, origin)` ordering — the higher version wins,
ties broken by origin string. This is automatic. Anti-entropy via `SyncWith` (manual) or
`WithDistMerkleAutoSync` (background) closes the gap. There is no manual reconciliation step today.

**Mitigation.** Run an odd number of nodes with quorum writes (`WithDistWriteConsistency(ConsistencyQuorum)`);
a partition that isolates a minority leaves only the majority side accepting writes because the minority
cannot reach quorum. The minority returns `ErrQuorumFailed` (`sentinel.ErrQuorumFailed`) on Set.

## Failure mode — hint queue overflow

**Symptom.** A peer is unreachable for a long time. Every replicated write to that peer turns into a queued
hint. Eventually the queue hits `WithDistHintMaxPerNode` or `WithDistHintMaxBytes` and new hints get dropped.

**Detection.** `dist.hinted.bytes` (gauge) climbs steadily. `dist.hinted.global_dropped` increments when caps
are exceeded. `dist.hinted.dropped` (a different metric — replay errors) also rises if the peer is reachable
but rejecting writes (auth, schema mismatch).

**Resolution.**

1. Restore the unreachable peer; the replay loop drains automatically (`dist.hinted.replayed` rises).
1. If the peer is permanently gone, remove it from membership (`DistMemory.RemovePeer(addr)`); queued hints
   expire on the `WithDistHintTTL` timer.
1. If hints are dropping faster than they replay, raise `WithDistHintMaxPerNode` / `WithDistHintMaxBytes` —
   but understand that the cap exists to bound process memory under sustained failure. Raising it without
   fixing the underlying peer just delays the bound.

**Phase B note.** Migration failures during rebalance now also funnel through the hint queue (Phase B.2). A
surge in `dist.hinted.queued` during a rolling deploy is expected; it should drain as the new node becomes
reachable.

## Failure mode — rebalance under load

**Symptom.** Adding a node triggers a rebalance scan that migrates keys to their new primary. Under sustained
write load the migration saturates and `dist.rebalance.throttle` increments — batches queue behind the
configured concurrency cap.

**Detection.** `dist.rebalance.last_ns` (gauge — last full scan duration) climbs. `dist.rebalance.throttle`
(counter) increments when the concurrency limit blocks a batch dispatch. `dist.rebalance.batches` should still
climb steadily.

**Resolution.**

1. Raise `WithDistRebalanceMaxConcurrent` (default 1) if CPU and network headroom allow.
1. Lower `WithDistRebalanceBatchSize` (default 64) so individual batches finish faster and concurrency slots
   cycle more often — counter-intuitively, smaller batches sometimes throughput-win.
1. Pause writes (drain a subset of clients via your LB) until the scan finishes. The dist backend has no
   built-in write-throttling — that's the application's job.

**Phase C note.** Drain (`POST /dist/drain`) does _not_ trigger an expedited rebalance today; the next
scheduled `WithDistRebalanceInterval` tick does the work. If you need to force a faster ownership transfer,
call `Stop` after Drain to cancel in-flight work and let restart-time rebalance handle migration.

## Failure mode — replica loss

**Symptom.** A replica node dies hard (kernel panic, hardware failure). Its keys still have other replicas
(when `replication >= 2`), but until membership notices, writes try to fan out to it and silently retry via
the hint queue.

**Detection.** `dist.heartbeat.failure` increments steadily for the lost peer. After `WithDistHeartbeat`'s
`deadAfter` window, the peer is pruned (`dist.nodes.removed` increments) and ring lookups stop including it.

**Resolution.**

1. Wait for the heartbeat to detect the dead peer. With default timing, this is on the order of seconds.
1. Spin up a replacement node with the same membership (or let gossip discover it).
1. The new node's rebalance scan pulls its assigned keys from surviving replicas via Merkle anti-entropy.

**Indirect probes.** `WithDistIndirectProbes(k, timeout)` filters caller-side network blips that would
otherwise mark a healthy peer suspect. `dist.heartbeat.indirect_probe.refuted` rising indicates indirect
probes are saving you from spurious flapping; rising `dist.heartbeat.indirect_probe.failure` indicates the
peer is genuinely unreachable from multiple vantage points.

## Operational tasks

### Drain a node

```sh
curl -X POST http://node-A:8080/dist/drain
```

After drain:

- `/health` returns 503; load balancers should stop routing.
- New `Set`/`Remove` calls return `sentinel.ErrDraining`.
- `Get` continues to serve until the process exits.

Drain is one-way. Restart the process to clear it.

### Inspect cluster state

```sh
# Membership snapshot.
curl http://node-A:8080/cluster/members

# Key enumeration (paginated, shard-by-shard since Phase C.2).
curl 'http://node-A:8080/internal/keys'
curl 'http://node-A:8080/internal/keys?cursor=1'
# ... follow next_cursor until empty.
```

### Force anti-entropy sync

```go
// Pull missing keys from peer "node-B" onto this node.
err := dm.SyncWith(ctx, "node-B")
```

`WithDistMerkleAutoSync(interval)` runs this on a timer; manual calls are useful for debugging.

## Capacity planning notes

- Each shard mutex is independent — write throughput scales with shard count up to CPU saturation.
- Hint queue memory is approximately `HintedBytes` + 64 bytes of bookkeeping per queued hint. Cap via
  `WithDistHintMaxBytes` to bound total process memory under partition.
- Merkle tree storage scales O(N/chunk) for N keys at `WithDistMerkleChunkSize` (default 128). For a million
  keys, the default chunk gives ~8K leaf hashes per node — negligible.
- Replication factor 3 with quorum reads/writes tolerates 1 failure; raise to 5 for tolerating 2 failures, at
  5× the storage cost.

## Where things are

| Concern             | File                                                                        |
| ------------------- | --------------------------------------------------------------------------- |
| Public surface      | [pkg/backend/dist_memory.go](../pkg/backend/dist_memory.go)                 |
| Transport interface | [pkg/backend/dist_transport.go](../pkg/backend/dist_transport.go)           |
| HTTP transport      | [pkg/backend/dist_http_transport.go](../pkg/backend/dist_http_transport.go) |
| HTTP server         | [pkg/backend/dist_http_server.go](../pkg/backend/dist_http_server.go)       |
| Membership / ring   | [internal/cluster/](../internal/cluster)                                    |
