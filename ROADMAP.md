# Distributed Backend Roadmap

This document tracks the evolution of the experimental `DistMemory` backend into a production‑grade multi‑node cluster in incremental, reviewable phases.

## Guiding Principles

- **Incremental**: Ship thin vertical slices; keep feature flags for rollback.
- **Deterministic**: Prefer explicit ownership calculations & version ordering.
- **Observable**: Every subsystem emits metrics/logs before being relied upon.
- **Fail Safe**: Degraded components (one node down) should not cascade failures.
- **Pluggable**: Transport, membership, serialization, and security are replaceable.

## Current State (Baseline – Updated)

Implemented:

- Consistent hashing ring (virtual nodes) + static membership.
- Replication factor & read/write consistency (ONE / QUORUM / ALL) with quorum enforcement.
- Versioning (Lamport-like counter) and read‑repair (targeted + full replica repair).
- Hinted handoff (TTL, replay interval, per-node & global caps, metrics, test-only helpers).
- Tombstones with TTL + compaction; anti-resurrection semantics.
- Merkle tree anti‑entropy (build + diff + pull) with metrics + periodic auto-sync.
- Management endpoints (`/cluster/*`, `/dist/*`, `/internal/merkle`, `/internal/keys`).
- Rebalancing (primary change + lost ownership migrations, batch + concurrency throttle metrics).
- Latency histograms (get/set/remove) snapshot API.
- Lightweight gossip snapshot exchange (in-process only).

Partial / In Progress:

- Failure detection (basic heartbeat, suspect/dead pruning; no indirect probes).
- Membership diffusion (gossip-lite; no full SWIM).
- Ownership migration (replica-only diff not yet supported).
- Adaptive anti-entropy scheduling (fixed interval currently).

Gaps / Planned:

- Replica-only ownership diff migrations.
- Migration retry queue & success/failure metrics.
- Incremental / adaptive Merkle scheduling & delete reconciliation matrix tests.
- Client SDK for direct owner routing.
- Advanced versioning (HLC / vector clocks).
- Tracing spans for distributed operations.
- Security (TLS/mTLS, auth) & compression.
- Chaos / latency / fault injection hooks.
- Persistence & durability (future consideration, not current scope).

## Phase Overview

### Phase 1: Data Plane & DistConfig (Weeks 1–2) – Status: DONE

Deliverables:

- `DistConfig` (NodeID, BindAddr, AdvertiseAddr, Seeds, ReplicationFactor, VirtualNodes, Hint settings, Consistency levels).
- HTTP JSON RPC endpoints: `POST /internal/set`, `GET /internal/get`, `DELETE /internal/del`.
- HTTP implementation of `DistTransport` (keep current in-process implementation for tests).
- Refactor DistMemory forwarding to use transport abstraction seamlessly.
- Multi-process integration test (3 nodes) verifying quorum & hint replay.

Metrics:

- Add latency histograms for set/get/del.

Success Criteria:

- Cross-process quorum & hinted handoff tests pass without code changes except wiring config.

### Phase 2: Failure Detection & Dynamic Membership (Weeks 3–4) – Status: PARTIAL

Deliverables:

- Heartbeat loop with optional random peer sampling (`WithDistHeartbeatSample`) and configurable interval. (Implemented)
- Node state transitions: alive → suspect → dead (timeouts & probe-driven escalation) with metrics for suspect/dead transitions. (Implemented)
- Ring rebuild on state change (exclude dead nodes). (Implemented)
- Global hint queue caps (count + bytes) with drop metrics (`WithDistHintMaxTotal`, `WithDistHintMaxBytes`). (Implemented)

Metrics:

- Heartbeat successes/failures, suspect/dead counters, membership version, global hint drops, approximate queued hint bytes. (Partially implemented; membership version exposed via snapshot API.)

Success Criteria:

- Simulated node failure triggers quorum degradation & hinting; recovery drains hints. (Covered by failure recovery & hint cap tests.)

### Phase 3: Rebalancing & Key Transfer (Weeks 5–6) – Status: PARTIAL

Deliverables:

- Ownership diff algorithm (old vs new ring).
- Batched key transfer (scan source owners; preserve versions & tombstones).
- Rate limiting & concurrent batch cap.
- Join/leave integration tests (distribution variance <10% of ideal after settle).

Metrics:

- Keys transferred, transfer duration, throttle events.

Success Criteria:

- Newly joined node receives expected shard of data; leaves do not resurrect deleted keys.

### Phase 4: Anti-Entropy Hardening (Weeks 7–8) – Status: PENDING

Deliverables:

- Incremental / windowed Merkle scheduling with adaptive backoff.
- Tombstone & delete reconciliation test matrix.
- Read-repair batching + metric for repairs applied.
- Optional fast-path hash (rolling / bloom) for clean shard skip.

Success Criteria:

- Injected divergences converge within configured interval (< target).

### Phase 5: Client SDK & Performance (Weeks 9–10) – Status: PENDING

Deliverables:

- Go client: seed discovery, ring bootstrap, direct owner hashing, parallel fan-out for QUORUM/ALL.
- Benchmarks: proxy path vs client-direct (latency reduction target >15%).
- Optional message serialization toggle (JSON/msgpack).

Success Criteria:

- QUORUM Get/Set p95 latency improved vs proxy path.

### Phase 6: Security & Observability (Weeks 11–12) – Status: PENDING

Deliverables:

- TLS enablement (cert config); optional mTLS.
- Pluggable auth (HMAC/Bearer) middleware for data RPC.
- OpenTelemetry spans: Set, Get, ReplicaFanout, HintReplay, MerkleSync, Rebalance.
- Structured logging (node id, trace id, op fields).

Success Criteria:

- End-to-end trace present for a Set with replication fan-out.

### Phase 7: Resilience & Chaos (Weeks 13–14) – Status: PENDING

Deliverables:

- Fault injection hooks (drop %, delay, partition simulation inside transport).
- Chaos tests (latency spikes, packet loss, partial partitions).
- Long-running stability test (memory growth bounded; no unbounded queues).

Success Criteria:

- Under 10% injected packet loss, quorum failure rate within acceptable SLO (<2% for QUORUM writes).

## Cross-Cutting Items

- Documentation updates per phase (`README`, `docs/distributed.md`).
- CI enhancements: integration cluster spin-up, race detector, benchmarks.
- Metric name stability & versioning (prefix `hypercache_dist_`).
- Feature flags / env toggles for new subsystems (gossip, rebalancing, anti-entropy scheduling).

## KPIs

| KPI | Target |
|-----|--------|
| QUORUM Set p95 (3-node HTTP) | < 3x in-process baseline |
| QUORUM Get p95 | < 2x in-process baseline |
| Hint Drain Time (single node outage 5m) | < 2m after recovery |
| Data Imbalance Post-Join | < 10% variance from ideal |
| Divergence Convergence Time | < configured sync interval |
| Quorum Failure Rate (1 node down, QUORUM) | < 2% |

## Immediate Next Actions (Short-Term Focus)

1. Implement replica-only ownership diff & migration during rebalance.
2. Add migration retry queue + metrics (success, failure, retries, drops).
3. Introduce adaptive Merkle scheduling (skip or backoff after clean cycles).
4. Instrument tracing spans (placeholders) for distributed operations.
5. Add chaos hooks (latency / drop %) to transport for resilience tests.

---

This roadmap will evolve; adjustments captured via PR edits referencing this file.
