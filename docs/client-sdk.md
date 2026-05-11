---
title: Client SDK
description: Go SDK for hypercache-server clusters — multi-endpoint HA, typed errors, and four authentication modes.
---

# Client SDK

Go client for `hypercache-server` clusters. Closes the three operational
gaps the OIDC example surfaced: every consumer used to hand-roll HTTP,
single-endpoint clients had no high availability, and there was no
username/password auth path for Redis-shop muscle memory. Wire-protocol
unchanged — the SDK speaks the same REST API that
[every node serves at `/v1/openapi.yaml`](api.md).

## Quickstart

```go
import (
    "context"
    "log"
    "os"
    "time"

    "github.com/hyp3rd/hypercache/pkg/client"
)

func main() {
    c, err := client.New(
        []string{"https://cache-0.example.com:8080", "https://cache-1.example.com:8080"},
        client.WithBearerAuth(os.Getenv("HYPERCACHE_TOKEN")),
        client.WithTopologyRefresh(30 * time.Second),
    )
    if err != nil {
        log.Fatal(err)
    }
    defer c.Close()

    ctx := context.Background()

    err = c.Set(ctx, "session:user-42", []byte("payload"), 5*time.Minute)
    if err != nil {
        log.Fatal(err)
    }

    value, err := c.Get(ctx, "session:user-42")
    if err != nil {
        log.Fatal(err)
    }

    log.Println(string(value))
}
```

That's the canonical shape. Any cluster reachable at one of the seed
endpoints will accept this; topology refresh discovers peers the seed
list doesn't mention; bearer/Basic/OIDC are swap-in alternatives below.

## Authentication

Four auth modes coexist on the server (`pkg/httpauth/policy.go` resolves
them in the order bearer → Basic → mTLS → OIDC). The SDK exposes three
of them as Option helpers; mTLS users supply a pre-configured
`*http.Client` via `WithHTTPClient`.

Applying multiple auth options keeps the **last one applied** — the
underlying `http.Client.Transport` is replaced wholesale on each call.

### Static bearer token

```go
client.WithBearerAuth(os.Getenv("HYPERCACHE_TOKEN"))
```

For tokens served from `HYPERCACHE_AUTH_CONFIG`'s `tokens:` block.
Static — the SDK does not refresh; use OIDC for short-lived tokens.

### HTTP Basic (Redis-style `AUTH user pass`)

```go
client.WithBasicAuth("svc-billing", os.Getenv("CACHE_PASSWORD"))
```

For credentials served from `HYPERCACHE_AUTH_CONFIG`'s `users:` block
(bcrypted server-side; see the
[server README](../cmd/hypercache-server/README.md) for the YAML shape).

The server refuses Basic over plaintext by default — make sure your
endpoint URLs are `https://`, or set `allow_basic_without_tls: true`
in the auth config for dev stacks. The SDK does not enforce TLS
client-side; the server does.

### OIDC client credentials

```go
import "golang.org/x/oauth2/clientcredentials"

client.WithOIDCClientCredentials(clientcredentials.Config{
    ClientID:     os.Getenv("OIDC_CLIENT_ID"),
    ClientSecret: os.Getenv("OIDC_CLIENT_SECRET"),
    TokenURL:     tokenURL, // resolve from .well-known/openid-configuration
    Scopes:       []string{"openid"},
    EndpointParams: url.Values{
        "audience": {os.Getenv("OIDC_AUDIENCE")},
    },
})
```

Wraps the standard `oauth2/clientcredentials` flow. Tokens are cached
in memory and transparently refreshed before expiry.

**The `audience` parameter is non-obvious.** Most IdPs (Auth0, Okta,
Keycloak with the audience mapper) require it at token-exchange time
for the resulting JWT's `aud` claim to populate to a value the cache's
verifier will accept. Set it via `EndpointParams`, not `Scopes`. See
the [OIDC example](../__examples/distributed-oidc-client/) for the
full discovery flow that produces `tokenURL`.

### Custom HTTP client (mTLS, custom transport)

```go
tlsConfig := &tls.Config{...} // your CAs, cert, key
client.WithHTTPClient(&http.Client{
    Transport: &http.Transport{
        TLSClientConfig: tlsConfig,
        MaxIdleConnsPerHost: 10,
    },
    Timeout: 10 * time.Second,
})
```

The escape hatch for everything the dedicated auth options don't
cover. Apply this **before** any other auth option if you want both
mTLS and bearer/Basic/OIDC layered on top — the auth options wrap
the existing Transport.

## Multi-endpoint high availability

Pass a slice of seed URLs to `New`. The SDK picks one at random for each
request; on retryable failure (network error, 5xx, 503 draining) it walks
to the next. On 4xx (auth, scope, not-found, bad-request) it returns
immediately — those answers are deterministic across the cluster and
retrying would only slow the caller.

```go
c, _ := client.New(
    []string{
        "https://cache-0.example.com:8080",
        "https://cache-1.example.com:8080",
        "https://cache-2.example.com:8080",
    },
    client.WithBearerAuth(token),
)
```

When every endpoint fails, the returned error wraps
`client.ErrAllEndpointsFailed` and the final `*StatusError` is reachable
via `errors.As` for inspection.

### Failover policy reference

| Outcome from endpoint     | Action          |
| ------------------------- | --------------- |
| Network error / timeout   | Fail over       |
| HTTP 5xx                  | Fail over       |
| HTTP 503 (draining)       | Fail over       |
| HTTP 401 / 403 / 404 / 4xx| Return to caller |
| HTTP 2xx                  | Return success  |

This is conservative by design — if a 401 propagated through failover,
a misconfigured token would burn every endpoint's auth budget before
surfacing.

## Topology refresh

Without refresh, the seed list is the entire view of the cluster for
the Client's lifetime. New nodes added after deploy stay invisible.
`WithTopologyRefresh(interval)` enables a background loop that pulls
`/cluster/members` from any reachable endpoint and replaces the
in-memory view with the alive-or-suspect members' API addresses.

```go
client.WithTopologyRefresh(30 * time.Second)
```

The seed list is **never lost** — if a refresh produces an empty view
(every known endpoint unreachable during a partition), the client falls
back to the original seeds. This is the recovery anchor; without it, a
partition that briefly nulled the working view would strand the client
permanently.

**Floor: 1 second.** Refresh intervals below 1s are rejected at
construction. `/cluster/members` serializes a full membership snapshot;
hammering it faster than 1s adds more load than the refresh saves.

For manual refresh in tests or operator-driven scenarios (post-deploy
"learn the new node now" sequences), call `c.RefreshTopology(ctx)`
synchronously.

## Errors

Every command method returns an error that satisfies `errors.Is`
against the package's sentinel set. The underlying `*StatusError`
carries the cache's canonical `{ code, error, details }` envelope
for callers that need finer discrimination via `errors.As`.

### Sentinels

| Sentinel                          | When it matches                                              |
| --------------------------------- | ------------------------------------------------------------ |
| `client.ErrNotFound`              | Key missing (404 / NOT_FOUND)                                |
| `client.ErrUnauthorized`          | Credentials rejected (401 / UNAUTHORIZED)                    |
| `client.ErrForbidden`             | Credentials valid but missing scope (403)                    |
| `client.ErrDraining`              | Every endpoint reported 503 / DRAINING                       |
| `client.ErrBadRequest`            | Malformed request shape (400 / BAD_REQUEST)                  |
| `client.ErrInternal`              | Cluster-side 5xx (500 / INTERNAL)                            |
| `client.ErrAllEndpointsFailed`    | Failover exhausted every endpoint                            |
| `client.ErrNoEndpoints`           | `New` called with empty seed slice (construction-only)       |

### Recipes

**Most common path — sentinel match:**

```go
value, err := c.Get(ctx, key)
if errors.Is(err, client.ErrNotFound) {
    // miss path
    return cacheMiss(key)
}
if err != nil {
    return err
}
```

**When you need `.Code` or `.Details`:**

```go
err := c.Set(ctx, key, value, ttl)

var se *client.StatusError
if errors.As(err, &se) {
    log.Warnf("cache rejected write: code=%s details=%s", se.Code, se.Details)
}
```

**When failover exhausts every endpoint:**

```go
err := c.Get(ctx, key)
if errors.Is(err, client.ErrAllEndpointsFailed) {
    var se *client.StatusError
    if errors.As(err, &se) {
        // se.Code is from the LAST endpoint we tried.
        log.Errorf("cluster appears down; last status: %s", se.Code)
    }
}
```

## Commands

| Method                              | Returns           | Notable errors                                |
| ----------------------------------- | ----------------- | --------------------------------------------- |
| `Set(ctx, key, value, ttl)`         | `error`           | `ErrForbidden`, `ErrBadRequest`               |
| `Get(ctx, key)`                     | `[]byte, error`   | `ErrNotFound`                                 |
| `GetItem(ctx, key)`                 | `*Item, error`    | `ErrNotFound`; `Item` carries metadata        |
| `Delete(ctx, key)`                  | `error`           | Idempotent — missing key is not an error      |
| `Identity(ctx)`                     | `*Identity, error`| `ErrUnauthorized` if the token is invalid     |
| `Endpoints()`                       | `[]string`        | Current view (post-refresh)                   |
| `RefreshTopology(ctx)`              | `error`           | Manual refresh — usually called by the loop   |
| `Close()`                           | `error`           | Stops the refresh loop; idempotent            |

`*Item` carries the full envelope — `Value` (raw bytes; base64
unwound for you), `Version`, `Owners`, `Node`, `ExpiresAt`. Use
`Get` when you only need bytes; `GetItem` when you need metadata.

`*Identity` carries `ID`, `Scopes`, and `Capabilities`. The
canonical canary at startup:

```go
id, err := c.Identity(ctx)
if err != nil {
    log.Fatalf("auth doesn't work: %v", err)
}
if !id.HasCapability("cache.write") {
    log.Fatal("this credential cannot write")
}
```

Prefer `HasCapability("cache.write")` over `slices.Contains(id.Scopes, "write")` — capability strings stay stable if a scope is later split
across multiple capabilities, while raw scope checks break on the
rename.

## Production caveats

The SDK is intentionally a thin layer over `net/http`. It does NOT
provide retry-with-backoff, connection pooling beyond what
`http.Transport` already does, or distributed-tracing instrumentation.
Those concerns live in the caller:

- **Pool HTTP connections** by passing a tuned `*http.Transport` via
  `WithHTTPClient`. Defaults are fine for low-throughput workloads;
  high-throughput callers will want `MaxIdleConnsPerHost` and
  `IdleConnTimeout` set explicitly.
- **Retry policy.** The SDK fails over across endpoints for one
  request; it does NOT retry the request itself after exhausting them.
  Wrap the call in a bounded exponential-backoff helper if you want
  retry semantics across `ErrAllEndpointsFailed`.
- **Observability.** Propagate trace context by setting your tracing
  middleware on the request context — `context.WithValue(ctx, ...)`
  flows into the `http.Request.Context()` and the cache server's
  OTel tracer picks up the `traceparent` header if your transport
  adds one. The SDK itself does not add OTel instrumentation.
- **Token-refresh visibility.** `WithOIDCClientCredentials` refreshes
  silently — there's no log when a token rotates. If you're debugging
  "why are my requests suddenly 401?", set `WithLogger(logger)` and
  watch the Debug-level lines for refresh activity.

## See also

- [`__examples/distributed-oidc-client/`](https://github.com/hyp3rd/hypercache/tree/main/__examples/distributed-oidc-client) — the SDK in action.
- [`__examples/distributed-oidc-client-raw/`](https://github.com/hyp3rd/hypercache/tree/main/__examples/distributed-oidc-client-raw) — the
  hand-rolled HTTP version. Useful when you need to understand what
  wire bytes the SDK is sending.
- [API reference](api.md) — the OpenAPI spec the SDK implements.
- [On-call cheatsheet — auth failures](oncall.md#auth-failures) —
  debugging 401/403s.
- [RFC 0003](rfcs/0003-client-sdk-and-redis-style-affordances.md) —
  the design decisions behind the SDK shape.
- Package source: [`pkg/client/`](https://github.com/hyp3rd/hypercache/tree/main/pkg/client).
