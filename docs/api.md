---
title: API Reference
description: Interactive OpenAPI 3.1 reference for the hypercache-server client REST API.
---

# API Reference

The HyperCache server exposes a REST API for application traffic on the
client port (default `:8080`). The interactive reference below is the
**same** OpenAPI 3.1 spec the binary embeds at build time and serves at
`GET /v1/openapi.yaml` — so the contract you read here is exactly what
the deployed cluster will honour.

!!! tip "Self-describing servers"

```text
    Every running node serves its spec at `/v1/openapi.yaml`. Point your
    own tooling at that URL to generate clients, run conformance checks,
    or render this same UI against a live cluster.
```

## Auth

When the server is started with `HYPERCACHE_AUTH_TOKEN` set, every
endpoint requires `Authorization: Bearer <token>`. Without that env
var, the API is open. Use the **Authorize** button in the UI below to
inject the header for live "Try it out" calls.

## Wire encoding

* Single-key `GET /v1/cache/{key}` returns raw bytes
  (`application/octet-stream`) by default for binary fidelity. Send
  `Accept: application/json` to receive an `ItemEnvelope` with metadata
  and a base64-encoded value.
* Batch endpoints always emit base64 values for binary safety.
* Errors carry the canonical `ErrorResponse` shape with stable
  machine-readable `code` strings (`BAD_REQUEST`, `NOT_FOUND`,
  `DRAINING`, `INTERNAL`, `UNAUTHORIZED`).

## Interactive reference

<swagger-ui src="openapi.yaml"></swagger-ui>

## Downloading the spec

The raw YAML lives in the repo at
[cmd/hypercache-server/openapi.yaml](../cmd/hypercache-server/openapi.yaml)
and is served by every node at `GET /v1/openapi.yaml`. Use it as the
input to client-codegen tools (`openapi-generator`, `oapi-codegen`,
`@redocly/cli`, …).
