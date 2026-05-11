# Distributed cache client with OIDC auth

A runnable example showing a backend service connecting to a `hypercache-server` cluster, authenticating via
OIDC client credentials, and exercising the full client API.

This is a **service-to-service** flow (no browser, no user redirect). The application proves its identity to
the IdP with a client ID and secret, gets back a short-lived JWT, and presents that JWT to the cache. The
cache validates the JWT against the same IdP and resolves the caller's identity + scopes. The same model fits
Keycloak, Auth0, Okta, Entra ID, Google, and any RFC 6749 §4.4 compliant IdP.

## What the example does

1. Discovers the IdP's token endpoint from `$OIDC_ISSUER/.well-known/openid-configuration`.
1. Uses `golang.org/x/oauth2/clientcredentials` to exchange the client ID + secret for an access token. The
   library caches the token in memory and transparently refreshes it before expiry — every cache call below is
   a plain `http.Client.Do` with no header bookkeeping.
1. Hits `GET /v1/me` to verify the bound identity + scopes (canary: "is my token actually valid against this
   cluster?").
1. Exercises `PUT /v1/cache/:key`, `GET` with raw bytes, `GET` with the JSON envelope (metadata view),
   `DELETE`, and `POST /v1/cache/batch/put`.

## Requirements

- Go 1.26+ (see [`go.mod`](../../go.mod))
- A reachable `hypercache-server` running with OIDC enabled (i.e. `HYPERCACHE_OIDC_ISSUER` and
  `HYPERCACHE_OIDC_AUDIENCE` set on the server). See
  [`cmd/hypercache-server/README.md`](../../cmd/hypercache-server/README.md).
- An OIDC client registered in your IdP with -. The **client_credentials** grant type enabled. -. A scope (or
  audience claim mapper) that produces the scopes the cache expects — see [Scope mapping](#scope-mapping)
  below.

## Environment variables

| Variable              | Required | Default                 | Description                                                              |
| --------------------- | -------- | ----------------------- | ------------------------------------------------------------------------ |
| `HYPERCACHE_ENDPOINT` | no       | `http://localhost:8080` | Cache server base URL (client API port).                                 |
| `OIDC_ISSUER`         | **yes**  | —                       | IdP base URL (no trailing `/.well-known`).                               |
| `OIDC_AUDIENCE`       | **yes**  | —                       | Must match the server's `HYPERCACHE_OIDC_AUDIENCE`.                      |
| `OIDC_CLIENT_ID`      | **yes**  | —                       | OAuth2 client ID registered for this service in the IdP.                 |
| `OIDC_CLIENT_SECRET`  | **yes**  | —                       | OAuth2 client secret. Treat as a secret — never commit.                  |
| `OIDC_SCOPES`         | no       | `openid`                | Space-separated scope list. See [Scope mapping](#scope-mapping).         |
| `OIDC_TOKEN_INSECURE` | no       | `0`                     | Set to `1` to skip TLS verification on the token endpoint. **Dev only.** |

## Run

```sh
export HYPERCACHE_ENDPOINT=https://cache.example.com:8080
export OIDC_ISSUER=https://keycloak.example.com/realms/cache
export OIDC_AUDIENCE=hypercache-cluster
export OIDC_CLIENT_ID=my-service
export OIDC_CLIENT_SECRET=...
export OIDC_SCOPES="openid cache:read cache:write"

go run ./__examples/distributed-oidc-client/
```

Expected output:

```text
== /v1/me (verify bound identity) ==
  resolved identity: my-service
  granted scopes:    [read write]

== PUT /v1/cache/example-key (5 min TTL) ==
  stored

== GET /v1/cache/example-key (raw bytes) ==
  value: "hello from oidc client"

== GET /v1/cache/example-key (JSON envelope) ==
  key:      example-key
  version:  1
  owners:   [cache-0 cache-1 cache-2]
  encoding: base64

== DELETE /v1/cache/example-key ==
  deleted

== batch PUT /v1/cache/batch/put (3 keys) ==
  stored 3 keys
```

## Scope mapping

The cache treats scopes as a closed set: `read`, `write`, `admin`. Your IdP's scope/claim values must map to
those three strings for the cache to grant access.

Two configuration knobs on the server control this mapping:

- `HYPERCACHE_OIDC_SCOPE_CLAIM` (default `scope`) — which JWT claim to read. Standard OAuth2 uses `scope`
  (space-separated string); some IdPs use a custom array claim like `cache_scopes`.
- The values inside that claim must be exactly `read`, `write`, or `admin`. Anything else is dropped silently.

**Pattern 1: OAuth2 standard scopes.** Register scopes `read` and `write` in your IdP, grant them to the
service client, and request them via `OIDC_SCOPES="openid read write"`. The cache reads them from the standard
`scope` claim.

**Pattern 2: Mapped scopes** (when your IdP namespaces scopes, e.g. `cache:read`). Use the IdP's claim-mapper
feature to project the `cache:read` scope into a custom claim, then set
`HYPERCACHE_OIDC_SCOPE_CLAIM=cache_scopes` server-side. Map `cache:read` → `read`, `cache:write` → `write` at
the mapper level.

**Pattern 3: Role-based.** Map IdP roles (e.g. Keycloak realm roles `cache-reader`, `cache-writer`) into the
custom claim. Same shape as Pattern 2.

## Coexistence with static bearer tokens

A cluster can run with both OIDC verification and static bearer tokens configured at the same time. The auth
chain resolves in this order:

1. `Authorization: Bearer <token>` matched against `HYPERCACHE_AUTH_CONFIG`'s `tokens` list → static identity.
1. If no static match, the OIDC verifier runs → OIDC identity.
1. If neither matches and `AllowAnonymous: true`, request runs as anonymous. Otherwise 401.

This means a single deployment can have humans signing in via OIDC (through the monitor's redirect flow) and
machine integrations using long-lived static bearers — both succeed against the same cache. See
[`pkg/httpauth/policy.go`](../../pkg/httpauth/policy.go) for the implementation.

## Production caveats

This example is intentionally minimal. For real services:

- **Pool HTTP connections.** `oauth2.NewClient` returns a fresh `http.Client`; in production you want a
  configured `Transport` with `MaxIdleConnsPerHost`, keepalives, and connection-level timeouts.
- **Retry policy.** This example does no retries; transient 5xx or network errors surface as failures. Wrap
  the cache calls in a bounded exponential-backoff retry for production.
- **Observability.** The cache emits OpenTelemetry traces if the server is configured with a tracer provider;
  propagate the trace context by setting `traceparent` headers on your requests.
- **Token caching across processes.** `clientcredentials` caches tokens per-process. If your service spawns
  many short-lived workers, consider a shared cache (e.g. Redis-backed) to avoid re-hitting the IdP on every
  process start.

## See also

- [`docs/auth.md`](../../docs/auth.md) — server-side auth surface (when present; otherwise
  [`cmd/hypercache-server/README.md`](../../cmd/hypercache-server/README.md) is the current source of truth).
- [`docs/oncall.md`](../../docs/oncall.md#auth-failures) — debugging 401/403s when the client surface is
  misbehaving.
- [`cmd/hypercache-server/oidc.go`](../../cmd/hypercache-server/oidc.go) — the cache's OIDC verifier closure,
  for reference on what's enforced server-side.
