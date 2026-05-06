---
title: Kubernetes (Helm)
---

# Kubernetes via Helm

The repo ships a Helm chart at
[`chart/hypercache/`](https://github.com/hyp3rd/hypercache/tree/main/chart/hypercache)
that produces a production-shaped k8s deployment: StatefulSet for stable
per-pod identity, headless Service for peer DNS, separate client and
management Services, a PodDisruptionBudget that holds quorum during
voluntary disruptions, and a hardened pod-security context.

## Why StatefulSet, not Deployment

Each peer's seed list pre-binds the others by
`<podname>.<headless-svc>.<ns>.svc.cluster.local`. A Deployment's pod
names are random suffixes, which would force a runtime peer-discovery
loop the dist HTTP transport doesn't have. StatefulSets give the
deterministic hostnames the seed format needs.

## Install

From a checkout:

```sh
helm install hyc chart/hypercache \
  --namespace hyc-prod --create-namespace
```

Default values produce 5 pods, replication factor 3, no auth, and a
ClusterIP-only client API. See [`values.yaml`][values] for the full
surface.

[values]: https://github.com/hyp3rd/hypercache/blob/main/chart/hypercache/values.yaml

## Common configuration

```sh
# Enable bearer-token auth via a chart-managed Secret.
helm install hyc chart/hypercache \
  --namespace hyc-prod --create-namespace \
  --set auth.token.value=$(openssl rand -base64 32)

# Use an operator-managed Secret (recommended for rotation).
helm install hyc chart/hypercache \
  --namespace hyc-prod --create-namespace \
  --set auth.token.existingSecret=hyc-token \
  --set auth.token.existingSecretKey=token

# Smaller cluster (3 pods, replication 2).
helm install hyc chart/hypercache \
  --namespace hyc-prod --create-namespace \
  --set replicaCount=3 --set cluster.replicationFactor=2 \
  --set podDisruptionBudget.minAvailable=2

# Expose the client API via LoadBalancer.
helm install hyc chart/hypercache \
  --namespace hyc-prod --create-namespace \
  --set service.client.type=LoadBalancer
```

## What gets created

Helm renders six resources by default (seven when auth is set inline):

| Resource | Name | Purpose |
|---|---|---|
| StatefulSet | `<release>-hypercache` | The pods themselves |
| Service (headless) | `<release>-hypercache-headless` | Per-pod DNS for peer discovery |
| Service | `<release>-hypercache` | Client API entry |
| Service | `<release>-hypercache-mgmt` | Management/observability |
| PodDisruptionBudget | `<release>-hypercache` | Holds quorum during drains |
| ServiceAccount | `<release>-hypercache` | Pod identity |
| Secret | `<release>-hypercache-auth` | (only when `auth.token.value` is set inline) |

## Probes

- **Liveness** hits the binary's `/healthz` on the client API port. If
  this fails, k8s restarts the pod — the Go runtime is dead.
- **Readiness** hits the dist HTTP `/health`. This endpoint flips to 503
  when an operator calls Drain, so a pod removed from rotation by
  `Drain` stops receiving Service traffic immediately, regardless of
  liveness state.

The binary runs SIGTERM → Drain → API stop → Cache.Stop with a 30 s
internal deadline; `terminationGracePeriodSeconds: 45` in the chart
gives it slack.

## Operations

The [runbook](operations.md) has split-brain, hint-queue overflow,
rebalance-under-load, and replica-loss procedures. Every failure mode
is mapped to a metric exposed by the management HTTP server (or the
OpenTelemetry pipeline you wire via `WithDistMeterProvider`).
