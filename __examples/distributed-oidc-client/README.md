# Distributed cache client with OIDC auth (SDK version)

The recommended Go consumer shape: import `pkg/client`, configure OIDC
client-credentials, dispatch commands. The SDK absorbs HTTP construction,
auth-header injection, token refresh, endpoint failover, topology refresh,
content negotiation, and typed errors — everything the
[raw HTTP version](../distributed-oidc-client-raw/) does by hand.

For the full SDK reference (every option, every error sentinel, every
production caveat) see [`docs/client-sdk.md`](../../docs/client-sdk.md).

## Environment variables

| Variable                | Required | Default                 | Description                                                              |
| ----------------------- | -------- | ----------------------- | ------------------------------------------------------------------------ |
| `HYPERCACHE_ENDPOINTS`  | no       | `http://localhost:8080` | Space-separated base URLs (seed list — the SDK fails over between them). |
| `OIDC_ISSUER`           | **yes**  | —                       | IdP base URL (no trailing `/.well-known`).                               |
| `OIDC_AUDIENCE`         | **yes**  | —                       | Must match the server's `HYPERCACHE_OIDC_AUDIENCE`.                      |
| `OIDC_CLIENT_ID`        | **yes**  | —                       | OAuth2 client ID registered for this service in the IdP.                 |
| `OIDC_CLIENT_SECRET`    | **yes**  | —                       | OAuth2 client secret. Treat as a secret — never commit.                  |
| `OIDC_SCOPES`           | no       | `openid`                | Space-separated scope list. See raw README's [Scope mapping](../distributed-oidc-client-raw/README.md#scope-mapping) section. |

## Run

```sh
export HYPERCACHE_ENDPOINTS="https://cache-0.example.com:8080 https://cache-1.example.com:8080"
export OIDC_ISSUER=https://keycloak.example.com/realms/cache
export OIDC_AUDIENCE=hypercache-cluster
export OIDC_CLIENT_ID=my-service
export OIDC_CLIENT_SECRET=...
export OIDC_SCOPES="openid cache:read cache:write"

go run ./__examples/distributed-oidc-client/
```

## Expected output

```text
authed as my-service with [cache.read cache.write]
Get("example-key") = "hello from sdk"
deleted
```

The SDK quietly does multi-endpoint failover behind that output — kill one
of the endpoints listed in `HYPERCACHE_ENDPOINTS` and the same run still
succeeds against the survivor.

## What's different from the raw version

| Concern               | Raw version                                                       | SDK version                                              |
| --------------------- | ----------------------------------------------------------------- | -------------------------------------------------------- |
| Lines of code         | ~480                                                              | ~150 (most of which is OIDC discovery + env wiring)      |
| Auth header injection | Custom RoundTripper                                               | `WithOIDCClientCredentials` does it                      |
| Token refresh         | `clientcredentials.TokenSource` (manual)                          | Same source, wrapped by the SDK                          |
| Endpoint failover     | None — single endpoint                                            | Random pick, fails over on 5xx / 503 / transport errors  |
| Topology refresh      | None                                                              | `WithTopologyRefresh(30s)`                               |
| Error discrimination  | Parse JSON envelope by hand                                       | `errors.Is(err, client.ErrNotFound)` etc.                |
| Content negotiation   | Manual `Accept: application/json` for envelope                    | `Get` (raw bytes) vs `GetItem` (envelope)                |

## See also

- [`docs/client-sdk.md`](../../docs/client-sdk.md) — full SDK reference.
- [`__examples/distributed-oidc-client-raw/`](../distributed-oidc-client-raw/) —
  the hand-rolled HTTP version. Useful when you need to understand exactly
  what wire bytes the SDK is sending.
- [`pkg/client/`](../../pkg/client/) — package source.
- [`docs/oncall.md#auth-failures`](../../docs/oncall.md#auth-failures) —
  debugging 401/403s when the client surface is misbehaving.
