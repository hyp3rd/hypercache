---
title: On-call cheatsheet
description: Symptom → log grep → metric → action map for HyperCache operators.
---

# On-call cheatsheet

You got paged. This page exists to take you from a symptom to a diagnosis in under sixty seconds. Each section
is a single failure shape: what you'll see, where to look, what to do next. Deeper operating procedures live
in the [operations runbook](operations.md); start here, descend there.

Every log line quoted below is a real string the binary emits — copy into `grep -F` directly. Every metric
name is from `DistMemory.Metrics()` and its OTel mirror (`dist.*`) or from the wrapper-level `StatsCollector`.

## Triage matrix

| Symptom                                          | Likely cause                                      | Jump to                                                   |
| ------------------------------------------------ | ------------------------------------------------- | --------------------------------------------------------- |
| Node won't start / never appears in cluster      | bind failure, bad config, OIDC issuer unreachable | [Node startup](#node-startup)                             |
| Cluster has the right members but cache is empty | new node still rebalancing in                     | [Cold replica](#cold-replica)                             |
| Peers flapping in `/cluster/members`             | network jitter, indirect probes failing           | [Heartbeat flapping](#heartbeat-flapping)                 |
| Hints building up faster than they drain         | one peer unreachable or rejecting writes          | [Hint queue](#hint-queue-building)                        |
| 401 / 403 on requests that should work           | misconfigured token, missing scope, OIDC expired, Basic over plaintext  | [Auth failures](#auth-failures)                           |
| Eviction running hot, latency spiking on Set     | cache at capacity, eviction can't keep up         | [Eviction pressure](#eviction-pressure)                   |
| Replicas diverging                               | partition healed, version conflicts               | [Split-brain reconciliation](#split-brain-reconciliation) |
| Drain stuck / load balancer still routing        | `/health` not flipping or LB caching              | [Drain not draining](#drain-not-draining)                 |

## Node startup

**What you'll see (good).** Exactly one of these on each node, in order, on every boot:

```text
msg="hypercache-server starting" api_addr=:8080 mgmt_addr=:8081 dist_addr=:7946 oidc_enabled=true
msg="cluster join: node starting" node_id=cache-0 replication=3 virtual_nodes=128 peers_known=4
msg="dist HTTP listener started" addr=:7946
msg="heartbeat loop started" interval=1s
msg="rebalance loop started" interval=30s
msg="hint replay loop started" interval=15s
```

If you see all six lines, the node has bound its ports, advertised itself to peers, and started its background
loops. Everything after this point is steady-state.

**What you'll see (bad).**

- `msg="dist HTTP listener bind failed"` — another process is already bound to `HYPERCACHE_DIST_ADDR`. Check
  for a stale pod / process on the host.
- `msg="oidc verifier construction failed"` — IdP discovery URL unreachable from the pod. Check
  `HYPERCACHE_OIDC_ISSUER`, DNS, and egress firewall rules. The process exits with code 1 (so the orchestrator
  will restart it; check `kubectl describe pod` for the loop).
- No `cluster join` line at all — the binary crashed before `buildHyperCache` returned. Look earlier in the
  log for `hypercache construction failed` with `err=...`.

**Metrics to check.** `dist.members.alive` (gauge) on every other node should tick up by one within
`WithDistHeartbeat`'s `aliveAfter` window. `dist.membership.version` increments on each membership change, so
it also bumps once per peer that learns about the new node.

## Cold replica

**What's happening.** A new replica is in the membership but its shards haven't been hydrated yet. Reads
against keys whose primary is elsewhere succeed (replica forward), but reads against keys this node should own
return misses until rebalance migrates them in.

**What you'll see in logs.** No errors — this is normal. After the first `rebalance loop started` line, expect
periodic ticks `rebalance.batches` increments visible at `/dist/metrics`.

**Metrics to check.**

- `dist.rebalance.batches` (counter) — incrementing means migration is happening.
- `dist.rebalance.keys` (counter) — total keys migrated this process-lifetime.
- `dist.rebalance.last_ns` (gauge) — duration of the last full scan. Compare to `WithDistRebalanceInterval` —
  if scan duration exceeds the interval, you have a sustained backlog.

**What to do.** Usually wait. If wait is unbounded, see
[Rebalance under load](operations.md#failure-mode-rebalance-under-load).

## Heartbeat flapping

**What's happening.** Peers cycle alive → suspect → alive every few ticks. Caller-side network jitter, an
overloaded probe path, or a mis-tuned `WithDistHeartbeat` are the usual causes.

**What you'll see in logs.**

```text
msg="peer marked suspect (timeout)" peer_id=cache-2 ...
msg="peer probe refuted by indirect probe" peer_id=cache-2 ...
msg="self-refuted suspect/dead claim from peer" ...
```

The third one is the recovery path — the suspected node observed itself being slandered and bumped its
incarnation to refute. If you see it landing, the SWIM dance is working as designed.

```text
msg="peer pruned (dead)" peer_id=cache-2 ...
msg="peer removed from membership" peer_addr=:7946 members_after=3
```

These two together mean a peer has been ejected — distinguish them from manual `RemovePeer` calls (which only
emit the second line, with no preceding `pruned (dead)`).

**Metrics to check.**

- `dist.heartbeat.failure` (counter) climbing — direct probes are failing.
- `dist.heartbeat.indirect_probe.refuted` (counter) — indirect probes are saving you from spurious flap.
  Healthy if non-zero.
- `dist.heartbeat.indirect_probe.failure` (counter) — indirect probes also fail. The peer is genuinely
  unreachable.
- `dist.nodes.suspect` / `dist.nodes.dead` (gauges) — current cluster state.

**What to do.** If `refuted` is climbing in step with `failure`, the system is self-correcting — extend
`WithDistHeartbeat`'s `suspectAfter` / `deadAfter` if the flap is noisy. If `indirect_probe.failure` is also
climbing, the peer is genuinely unreachable — see [replica loss](operations.md#failure-mode-replica-loss).

## Hint queue building

**What's happening.** A peer is unreachable. Every replicated write to it gets queued as a hint, waiting for
the peer to come back. The queue is bounded — see `WithDistHintMaxPerNode` / `WithDistHintMaxBytes`.

**What you'll see in logs.**

```text
msg="rebalance migration forward failed; queued for hint replay" target_addr=... err=...
msg="hint dropped after replay error" target_node=... err=...
```

The first is benign during a peer outage. The second means the peer came back but rejected the hint — auth
mismatch, schema drift, or a truly bad value.

**Metrics to check.**

- `dist.hinted.bytes` (gauge) — climbing steadily, no drain → peer still down.
- `dist.hinted.queued` (counter) — total ever queued; rising rate is the canary.
- `dist.hinted.replayed` (counter) — climbs when the peer is reachable and the queue is draining.
- `dist.hinted.global_dropped` (counter) — caps exceeded; hints are being silently dropped. Hard limit hit.
- `dist.hinted.expired` (counter) — hints aged past `WithDistHintTTL`.

**What to do.** See [Hint queue overflow](operations.md#failure-mode-hint-queue-overflow) for the full
playbook. Short version: restore the peer, or remove it from membership and let hints expire.

## Auth failures

**What's happening.** A request hit the API or management port without an identity that satisfies the policy.

**What you'll see in logs.** Auth failures are deliberately quiet (no "request denied" log per call — that
would be a log-spam amplifier). Look for the `audit` line emitted by the management HTTP layer on denied
access, and at `/dist/metrics` for `auth.*` counters if your build has them.

**What to check first.**

- `curl http://<node>:8081/v1/me -H 'Authorization: Bearer <token>'` → returns the resolved identity + scopes.
  If this returns 401, the token itself is wrong; if it returns 200 with empty scopes, the token resolves but
  lacks the scope the endpoint requires.
- For OIDC tokens: `aud` and `iss` must match `HYPERCACHE_OIDC_AUDIENCE` / `HYPERCACHE_OIDC_ISSUER`. The
  verifier rejects mismatches before any policy check runs.
- For static bearers: the token must appear in the policy YAML (`HYPERCACHE_AUTH_CONFIG`) — confirm with
  `curl http://<node>:8081/v1/me` using that exact token.
- For HTTP Basic (`users:` block): `curl -u <user>:<pass> https://<node>:8081/v1/me`. If 401 over plaintext
  HTTP, the server is refusing Basic-without-TLS by default — either upgrade to HTTPS or set
  `allow_basic_without_tls: true` in `HYPERCACHE_AUTH_CONFIG` for dev stacks (never production).
- The new `capabilities` field on `/v1/me` shows what the caller can DO (`cache.read`, `cache.write`,
  `cache.admin`) — clients should key off this, not the raw `scopes` array, for forward-compatibility.

**What to do.**

1. Reproduce with `curl /v1/me` (definitive truth — same chain that the failing endpoint runs).
1. If `/v1/me` returns 401: the token is rejected before reaching the scope check. Bearer mismatch, OIDC
   expiry, or revoked cert.
1. If `/v1/me` returns 200 but the original endpoint still 403s: the identity resolved but lacks the required
   scope. Check the route's `Scopes` mapping (`management_http.go`); cross-reference against the identity's
   `scopes` field in the `/v1/me` response.
1. For OIDC token expiry specifically — `exp` is in the JWT payload;
   `cut -d. -f2 <token> | base64 -d | jq .exp` decodes it client-side.

## Eviction pressure

**What's happening.** The cache is at or above capacity. Eviction is running on every tick, every `Set`
triggers an immediate evict, and `Set` latency reflects the eviction cost.

**What you'll see in logs.** With Info-level logging on, every tick that does work emits:

```text
msg="eviction tick" evicted=42 items_remaining=10000 elapsed=3.2ms
```

A sustained sequence of these (non-zero `evicted` on every tick) is the symptom. If you also see
`eviction triggered source=manual`, something is calling `TriggerEviction` from application code.

**Metrics to check.**

- `eviction_loop_count` (counter) — how often the loop ran.
- `item_evicted_count` (counter) — total items evicted.
- `evicted_item_count` (gauge) — items evicted in the **last** tick. Sustained non-zero = under pressure.
- `eviction_loop_duration` (timing) — tick latency. Climbing → eviction itself is the bottleneck.

**What to do.**

1. Raise capacity (`WithMaxCacheSize` or per-backend equivalent).
1. Audit `Set` callers — is something setting keys with no TTL and no key reuse? Eviction is doing the work
   TTL should.
1. Switch eviction algorithm — `WithEvictionAlgorithm("lru")` vs `"lfu"` vs `"cawolfu"` have very different
   working-set fit.
1. Increase `WithEvictionShardCount` (default 32) — eviction contention is per-shard.

## Split-brain reconciliation

**What's happening.** A partition healed. Both sides have writes the other doesn't.

**What you'll see in logs.** During the partition: heartbeat failure logs (see
[Heartbeat flapping](#heartbeat-flapping)). After healing: the merkle anti-entropy loop reconciles. No
specific log line is emitted per resolved conflict — version-and-origin ordering is silent by design (it would
log-spam under load).

**Metrics to check.**

- `dist.version.conflicts` (counter) — increments per detected divergence. Climbs after a heal, then
  stabilizes.
- `dist.merkle.last_diff_ns` (gauge) — duration of the last sync.
- `dist.merkle.syncs` (counter) — successful merkle pulls.
- `dist.merkle.keys_pulled` (counter) — keys reconciled.

**What to do.** Usually wait. Auto-sync drains on its `WithDistMerkleAutoSync` interval. To force-trigger:

```go
err := dm.SyncWith(ctx, "peer-node-id")
```

The full discussion is in [Split-brain](operations.md#failure-mode-split-brain).

## Drain not draining

**What's happening.** You posted to `/dist/drain`, but `/health` still returns 200, or the load balancer is
still routing.

**What you'll see in logs.**

```text
msg="dist node draining"
```

If you see this line exactly once after the `POST /dist/drain`, the drain registered cache-side. From here:

- `/health` returns **503** on every subsequent request.
- New `Set` / `Remove` return `sentinel.ErrDraining` (HTTP 503).
- `Get` continues to serve from cache.

**What to do.**

1. Confirm the drain line in the logs first. If absent, the request never reached the node — check the
   management address you're POSTing to (`HYPERCACHE_MGMT_ADDR`, not the client API port).
1. If the drain logged but `/health` still returns 200, you're probably hitting the wrong listener — `/health`
   lives on both the client API and management ports, and only the latter respects drain. Confirm via
   `curl -v http://<node>:8081/health` (mgmt) vs `:8080` (api).
1. If `/health` correctly returns 503 but the LB still routes, that's a load-balancer problem, not a cache
   problem. Check the LB's health-check cache TTL.

Drain is one-way per process; restart to clear.

## Structured-logging reference

Every log line the cache emits as of this writing, grouped by source. Use this as a grep dictionary.

### Lifecycle (`hypercache-server`)

| Message                                                             | When                             | Level |
| ------------------------------------------------------------------- | -------------------------------- | ----- |
| `hypercache-server starting`                                        | binary boot, once                | Info  |
| `hypercache-server running with no client API auth configured; ...` | misconfigured auth               | Warn  |
| `shutdown signal received`                                          | SIGINT/SIGTERM received          | Info  |
| `hypercache-server stopped cleanly`                                 | shutdown complete                | Info  |
| `oidc verifier construction failed`                                 | IdP unreachable at boot          | Error |
| `client API listener exited`                                        | API port goroutine died          | Error |
| `hypercache construction failed`                                    | wrapper init error               | Error |
| `client API construction failed`                                    | server init error                | Error |
| `drain returned error`                                              | drain attempt on shutdown failed | Warn  |
| `client API shutdown returned error`                                | graceful shutdown failed         | Warn  |
| `hypercache stop returned error`                                    | wrapper stop failed              | Warn  |

### Wrapper loops (`HyperCache`)

| Message                                        | When                                          | Level |
| ---------------------------------------------- | --------------------------------------------- | ----- |
| `eviction loop starting`                       | wrapper start, once if `evictionInterval > 0` | Info  |
| `eviction loop stopped`                        | context canceled or stop signal               | Info  |
| `eviction tick`                                | tick did work (evicted > 0)                   | Info  |
| `eviction tick (idle)`                         | tick ran with nothing to evict                | Debug |
| `eviction triggered`                           | `TriggerEviction()` accepted                  | Info  |
| `eviction trigger coalesced (already pending)` | trigger arrived while one in-flight           | Debug |
| `expiration loop starting`                     | wrapper start, once                           | Info  |
| `expiration loop stopped`                      | context canceled or stop signal               | Info  |
| `expiration tick`                              | tick removed expired items                    | Info  |
| `expiration tick (idle)`                       | tick ran with nothing expired                 | Debug |

### DistMemory backend

| Message                                                      | When                                 | Level |
| ------------------------------------------------------------ | ------------------------------------ | ----- |
| `cluster join: node starting`                                | DistMemory constructor, once         | Info  |
| `dist HTTP listener started`                                 | peer transport bound                 | Info  |
| `dist HTTP listener bind failed`                             | port in use / permission denied      | Error |
| `dist HTTP serve goroutine exited`                           | transport listener stopped           | Info  |
| `heartbeat loop started`                                     | SWIM probe loop start                | Info  |
| `gossip loop started`                                        | gossip push loop start               | Info  |
| `hint replay loop started`                                   | hint drain loop start                | Info  |
| `rebalance loop started`                                     | ownership-migration loop start       | Info  |
| `merkle auto-sync loop started`                              | anti-entropy loop start              | Info  |
| `peer added to membership`                                   | `AddPeer` accepted                   | Info  |
| `peer removed from membership`                               | `RemovePeer` or `peer pruned (dead)` | Info  |
| `peer marked suspect (timeout)`                              | direct probe failed                  | Warn  |
| `peer marked suspect (probe failed)`                         | probe error during SWIM              | Info  |
| `peer probe refuted by indirect probe`                       | indirect probe rescued the peer      | Warn  |
| `peer pruned (dead)`                                         | suspect window exceeded; ejected     | Warn  |
| `self-refuted suspect/dead claim from peer`                  | local incarnation bump               | Info  |
| `gossip push failed`                                         | gossip dispatch error                | Warn  |
| `merkle sync fetch failed`                                   | anti-entropy pull error              | Warn  |
| `rebalance migration forward failed; queued for hint replay` | replication during rebalance failed  | Warn  |
| `hint dropped after replay error`                            | hint replayed but peer rejected      | Info  |
| `dist node draining`                                         | `POST /dist/drain` accepted          | Info  |

### Telemetry registration

| Message                                    | When                                 | Level |
| ------------------------------------------ | ------------------------------------ | ----- |
| `dist meter: counter registration failed`  | OTel meter binding error             | Error |
| `dist meter: gauge registration failed`    | OTel meter binding error             | Error |
| `dist meter: callback registration failed` | OTel observable callback bind failed | Error |
| `dist meter: callback unregister failed`   | OTel meter teardown error            | Error |

## Quick filters

```sh
# All cluster-membership events for this node:
journalctl -u hypercache -o cat | grep -E 'peer (added|removed|marked|pruned|probe)'

# Background-loop health (every loop emits exactly one starting line per process):
journalctl -u hypercache -o cat | grep -F 'loop starting' | grep -F 'loop started'

# Hint-queue trouble (replay errors, drops):
journalctl -u hypercache -o cat | grep -F 'hint '

# All Warns and Errors only:
journalctl -u hypercache -o cat -p warning..err
```

## Going deeper

For the design background:

- [Distributed backend](distributed.md) — replication, hashing, membership.
- [Operations runbook](operations.md) — long-form failure-mode playbooks. Each `#failure-mode-*` anchor
  matches a symptom above.
- [API reference](api.md) — REST surface served by the binary.
