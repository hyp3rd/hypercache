# Distributed Backend (DistMemory) Deep Dive

This document captures the current state, design, limitations, and roadmap deltas for the experimental in‑process distributed backend `DistMemory`.

## High-Level Goals

Provide a feature playground to iterate on ownership, replication, consistency, and anti‑entropy mechanics before committing to a production multi‑process implementation (real gossip + RPC + resilience). The emphasis is on correctness observability and incremental layering.

## Implemented Capabilities

- Consistent hashing ring with virtual nodes (configurable replication factor).
- Versioning: lamport-like monotonic counter per process + origin tie-break.
- Read/Write consistency levels: ONE, QUORUM, ALL (quorum math: floor(R/2)+1).
- Forwarding & promotion: non-owner forwards to primary; promotes next owner if primary unreachable.
- Replica fan-out (synchronous for writes from promoted or primary path, best-effort for repairs / rebalancing).
- Quorum write acknowledgements with failure accounting.
- Read repair (targeted stale owner repair + full replica convergence pass for quorum/all reads).
- Hinted handoff with TTL, replay interval, per-node & global caps (count + bytes) + lifecycle metrics.
- Tombstones (versioned delete intents) with TTL + compaction and anti‑resurrection semantics.
- Merkle tree anti‑entropy (build, diff, pull) + metrics (fetch/build/diff nanos, pulled keys).
- Periodic auto Merkle sync with optional peer cap per tick.
- Heartbeat-based failure detection (alive→suspect→dead, prune) + metrics.
- Lightweight in-process gossip snapshot exchange (non-authoritative, best-effort convergence of membership records).
- Rebalancing (primary-change & full ownership loss migrations) with batching, concurrency cap, throttle metric, replica-only diff pushes, and grace-based shedding of keys no longer owned.
- Latency histograms for Get/Set/Remove (ns buckets) exposed via `LatencyHistograms()`.
- Management endpoints: owners, metrics, membership snapshot, ring dump, Merkle tree, key enumeration (debug), tombstone metrics.
- HTTP JSON transport abstraction (`DistHTTPTransport`) + in-process transport for tests.

## Metrics Overview (Key Subsets)

- Forwarding: `ForwardGet`, `ForwardSet`, `ForwardRemove`.
- Replication & Consistency: `WriteAttempts`, `WriteAcks`, `WriteQuorumFailures`, `ReadPrimaryPromote`.
- Repairs & Versioning: `ReadRepair`, `VersionConflicts`, `VersionTieBreaks`.
- Hinted Handoff: `HintedQueued`, `HintedReplayed`, `HintedExpired`, `HintedDropped`, `HintedGlobalDropped`, `HintedBytes`.
- Merkle: `MerkleSyncs`, `MerkleKeysPulled`, `MerkleBuildNanos`, `MerkleDiffNanos`, `MerkleFetchNanos`, `AutoSyncLoops`.
- Tombstones: `TombstonesActive`, `TombstonesPurged`.
- Rebalancing: `RebalancedKeys`, `RebalancedPrimary`, `RebalanceBatches`, `RebalanceThrottle`, `RebalanceLastNanos`, `RebalancedReplicaDiff`, `RebalanceReplicaDiffThrottle`.
- Membership State Snapshot: `MembershipVersion`, `MembersAlive`, `MembersSuspect`, `MembersDead`.

## Rebalancing Details

Current migration / replication triggers:

1. Node lost all ownership (no longer primary nor replica) for key (record timestamp for shedding).
2. Node was previously the recorded primary and current primary changed (increments `RebalancedPrimary`).
3. New replicas added while we remain primary (replica diff replication, per-tick capped).
4. Grace-elapsed keys we no longer own are deleted locally (shedding) if `WithDistRemovalGrace` set.

Limitations:

- No retry queue: migration is best-effort fire-and-forget (forward failures silent).
- Full shard scan every interval (O(N) per tick); future work: incremental token / cursor scanning.
- Shedding performs local deletion only (no tombstones emitted); late routed reads may rely on anti-entropy for convergence.

Configuration knobs:

- `WithDistRebalanceInterval(d)` – scan period.
- `WithDistRebalanceBatchSize(n)` – max keys per batch.
- `WithDistRebalanceMaxConcurrent(n)` – concurrent batch goroutines (bounded via semaphore).
- `WithDistReplicaDiffMaxPerTick(n)` – cap replica-only diff replications per tick (0 = unlimited).
- `WithDistRemovalGrace(d)` – grace before local deletion of keys we no longer own (0 disables shedding).

## Tombstones & Delete Semantics

- Deletes allocate a tombstone version: previous item version +1; if absent, from per-process monotonic counter.
- Tombstones prevent lower/equal remote versions from resurrecting data.
- Merkle diff treats tombstone version as authoritative; remote absence vs local presence triggers inferred tombstone if newer local key missing remote counterpart.
- TTL + periodic sweep optionally compacts old tombstones; risk: if compaction happens before a previously partitioned replica resurfaces, a now-missing delete intent could allow stale resurrection (acceptable in experimental scope, documented for future durable design).

## Hinted Handoff

- Enqueue when a replica write fails with backend-not-found.
- Per-node FIFO queue capped by `WithDistHintMaxPerNode`; global caps via `WithDistHintMaxTotal` & `WithDistHintMaxBytes` (approximate serialized size heuristic).
- Replay loop attempts delivery; outcomes increment replay, expired, dropped, or global dropped metrics.
- Test-only helpers gated behind `//go:build test` tag allow forced replay & queue inspection.

## Anti-Entropy (Merkle)

- Tree built over key+version (tombstones included) chunked by `WithDistMerkleChunkSize`.
- Diff identifies differing leaf indexes; missing remote-only keys enumerated via in-process introspection or `/internal/keys` fallback (capped by `WithDistListKeysCap`).
- Remote fetch & adoption updates local versions; missing local items with remote deletion inferred produce local tombstones.
- Future: incremental scheduling (adaptive intervals based on recent diffs), deletion reconciliation matrix tests, rolling hash fast-path.

## Failure Detection & Membership

- Heartbeat loop probes peers (optionally sampling via `WithDistHeartbeatSample`).
- Timeouts mark nodes suspect, then dead; dead nodes pruned from membership map (ring rebuild via membership internals).
- Gossip loop periodically exchanges snapshots with one random peer (in-process transport only) to spread membership state.
- Lacks: full SWIM-style dissemination, incarnation conflict resolution rules, indirect probes, suspicion suppression.

## Latency Histograms

- Internal fixed-width ns buckets (implementation detail subject to change) per operation: get, set, remove.
- Snapshot via `LatencyHistograms()` returns a Go map of bucket counts (`map[string][]uint64`).
- Not yet exposed via external metrics exporter / OpenTelemetry.

## Limitations Summary

- Single process simulation (even HTTP transport resolves in-process in current tests).
- No persistent storage or WAL.
- No network partitions / latency injection (future chaos tooling).
- No tracing spans for distributed operations.
- Security (TLS/mTLS, auth) absent.
- Compression unsupported.
- Migration & repair actions are fire-and-forget (no retry backoff queues).
- Migration retry queue absent.

## Near-Term Roadmap Deltas

1. Migration retry queue + success/failure counters.
2. Incremental / adaptive Merkle scheduling (skip if repeated clean cycles).
3. Tracing spans (OpenTelemetry) for Set/Get/Repair/Merkle/Rebalance/HintReplay.
4. Enhanced failure detector (indirect probes, exponential backoff, state gossip).
5. Client SDK (direct owner routing; bypass proxy hop).
6. Chaos hooks (latency, drop %, partition segments) for test harness.

## Design Trade-offs

- Simplicity over perfect consistency: lamport + origin tie-break avoids vector clock overhead while enabling deterministic resolution.
- Tombstone monotonic counter (per process) defers cross-node version negotiations until more advanced clocks are introduced.
- Full-shard scans for rebalance are acceptable for moderate key counts; complexity postponed until ownership change frequency justified.
- Fire-and-forget migrations & repairs minimize tail latency but lack durability; acceptable while experimenting with correctness semantics.

## Contributing Guidance

When extending DistMemory:

- Favor introducing metrics before complex logic (observability first).
- Keep value-copy semantics for iteration snapshots to avoid pointer races.
- Guard new shared maps with RWMutex (pattern used for originalPrimary).
- Maintain test-only helpers behind build tags.
- Update `distributed.md`, `ROADMAP.md`, and README progress tables within the same PR.

---

DistMemory is intentionally experimental; treat its interfaces as unstable until promoted out of the feature branch.
