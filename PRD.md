# Distributed Multi-Node Cluster Backend PRD

We need to evolve from “multi-shard in one process” to “multi-node cluster”. Two core gaps: (1) node identity + membership, (2) a remote transport (RPC) so local instance can route/replicate operations.

Checklist (incremental roadmap)
Node identity & config
Membership (static bootstrap → dynamic gossip)
Consistent hashing ring (virtual nodes)
Replication & consistency knobs
RPC protocol (minimal HTTP/JSON first; pluggable to gRPC later)
Request routing (client side vs each node proxy)
Failure detection / tombstoning
Rebalancing & handoff
Background anti-entropy (repair)
Observability & ops endpoints
Pluggable serialization / compression
Security (TLS + auth) — later

1. Node Identity
Each process: NodeID (uuid or hash of host:port) + AdvertiseAddr + ClusterPort.
Config example: DistConfig{ NodeID, BindAddr, Seeds []string, ReplicationFactor, VirtualNodes }.
2. Membership
Phase 1 (static): Provide full seed list; build ring once. Phase 2 (gossip): Periodic heartbeat (UDP or lightweight TCP ping) + membership state (alive, suspect, dead) using SWIM-like protocol. Data structures:

membership.Map[NodeID] -> {State, Incarnation, Addr, LastHeartbeat}
event channel for ring rebuild.
3. Consistent Hashing Ring
Use virtual nodes (e.g., 100–200 per physical node) hashed into a sorted ring (uint64).
Key hash -> first vnode clockwise ⇒ primary. Next (R-1) distinct physical nodes ⇒ replicas.
Rebuild ring atomically when membership changes (copy-on-write).
4. Replication & Consistency
Start simple: quorum-less (write to all replicas async, read first that responds). Then add:

ReplicationFactor (R)
W (write quorum), RQ (read quorum) defaults (W=1,RQ=1) for availability; allow optional majority.
Versioning: per-item vector clock or simpler lamport + last-write-wins initially.
5. RPC Transport
MVP: HTTP JSON

POST /put {key, value, ttl, version}
GET /get?key=...
DELETE /del?key=... Internal header: X-HyperCache-NodeID. Later: switch to gRPC or custom binary for performance.
6. Routing
Client library can hash & send directly to primary+replicas (better latency). If not, any node accepts request:

If local node not responsible, it forwards (proxy) to primary and aggregates responses.
7. Failure Detection
Heartbeat every T (e.g., 1s) to k random peers.
Missed N heartbeats -> suspect; disseminate.
Additional misses -> dead; remove from ring (but keep for hinted handoff).
8. Rebalancing / Handoff
When node joins/leaves: determine key ranges moving.
Async pull: new owner requests batches (ListFiltered by hash range) from old owners.
Hinted handoff: if replica down during write, store hint locally; replay when node returns.
9. Anti-Entropy
Periodic Merkle tree or rolling hash per partition to detect divergence (later).
MVP: periodic full range scan diff (inefficient but simple for small sets).
10. Observability
Endpoints:

/cluster/members
/cluster/ring (hash ranges)
/cluster/statistics (replication lag, pending hints)
/cluster/health
11. Data Model Changes
Item metadata:

Version (uint64 or vector)
ReplicaSet (optional)
LastUpdated timestamp
12. Security (later)
TLS config + shared secret / mTLS.

Incremental Coding Plan (first 3 PR-sized steps)
Step A (Foundations):

New package cluster/: member.go, ring.go, hash.go
NodeID generation & static seed join
Build ring + local routing; still single process but infrastructure ready Step B (Networking):
Internal HTTP server exposing put/get/del for inter-node.
DistMemory upgraded: when Set/Get invoked, route to responsible nodes (still only 1 local node in tests). Step C (Replication & Multi-node tests):
Spin up 3 nodes in integration test, seed each other.
Implement simple R=2 replication (write both synchronously).
Add basic membership event (manual add) and ring rebuild.
Minimal Data Flow (MVP)
Set:

Hash(key) -> nodes
If local is primary: store locally, sync send to replicas (no quorum wait at start).
Return success after local + best-effort replicate.
Get:

Hash(key) -> nodes
Query local if owner; else forward to primary; fallback to replicas if miss.
Node Interaction (Your Questions)
Identify nodes:

NodeID (uuid) + AdvertiseAddr; all nodes share membership map via gossip or seed bootstrap.
Interact:

Cluster-aware client or any node HTTP API.
Management API extended with /cluster/* for status and possibly a /forward endpoint internally.
