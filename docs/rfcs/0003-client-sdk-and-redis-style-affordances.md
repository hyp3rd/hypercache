# RFC 0003 — Client SDK and Redis-style affordances

- **Status**: Open — Draft
- **Target**: Phase 5 (Client SDK & Performance) + cross-cutting auth/HA additions
- **Owners**: TBD
- **Related code**: [pkg/httpauth/policy.go](../../pkg/httpauth/policy.go),
  [cmd/hypercache-server/main.go](../../cmd/hypercache-server/main.go),
  [cmd/hypercache-server/oidc.go](../../cmd/hypercache-server/oidc.go),
  [pkg/backend/dist_memory.go](../../pkg/backend/dist_memory.go) — `/cluster/members` endpoint,
  [\_\_examples/distributed-oidc-client/main.go](../../__examples/distributed-oidc-client/main.go) — the demo
  this RFC supersedes

## Summary

Ship a Go client SDK for HyperCache that closes three operational gaps the OIDC-client example surfaced:

1. **Single-endpoint clients have no high availability.** A failed node takes down every consumer pointing at
   it. Operators must front the cluster with an LB they didn't ask for, or live with the outage. We want
   Redis/Valkey-style multi-endpoint clients that learn the cluster shape and fail over without operator
   intervention.
1. **No username/password auth.** The cache today offers static bearer tokens, OIDC JWTs, and mTLS — all good
   for cloud-native deployments but a poor fit for environments where Redis-style `AUTH user pass` is the
   established pattern.
1. **Every client re-implements the wire protocol.** The demo we shipped
   ([`__examples/distributed-oidc-client/main.go`](../../__examples/distributed-oidc-client/main.go)) landed
   at ~500 lines; ~200 of those are generic boilerplate every integrator will rewrite (HTTP construction,
   error envelope parsing, content negotiation, base64 batching). A proper SDK collapses that to ~30 lines of
   caller code.

This RFC enumerates the design choices for each gap and recommends a path. Implementation is a follow-up; the
document exists to surface trade-offs before code lands.

## Background

### The example audit

Building [`__examples/distributed-oidc-client/main.go`](../../__examples/distributed-oidc-client/main.go)
catalogued the friction points (full list in that PR's notes). The load-bearing ones for this RFC:

- The client takes a **single** `HYPERCACHE_ENDPOINT`. There is no fallback if it returns 503 (draining),
  times out, or refuses connections. The example doesn't even round-robin a list — there's nowhere in the wire
  protocol or the env-config shape for a list to go.
- The IdP needs an `audience` request parameter at token-exchange time for the resulting JWT's `aud` claim to
  populate. This is non-standard OAuth2 — it's an IdP extension. We don't document it anywhere. Every consumer
  rediscovers it.
- Scope mapping is opaque: the cache only honors `read`/`write`/`admin` as scope values; everything else is
  silently dropped. Operators either name OAuth scopes exactly those strings (collision-prone) or use
  IdP-specific claim mappers.
- The cache returns a structured error envelope (`{ code, error, details }`) with stable `code` strings, but
  there's no Go type to `errors.As` against. Every client parses + discriminates manually.

### What Redis/Valkey clients give consumers today

The expectations operators arrive with from go-redis / valkey-go:

| Affordance                 | Redis/Valkey                      | HyperCache today                                            |
| -------------------------- | --------------------------------- | ----------------------------------------------------------- |
| Multiple seed endpoints    | `Addrs: []string{...}`            | Single `HYPERCACHE_ENDPOINT`                                |
| Discovery of cluster shape | `CLUSTER NODES` periodically      | `/cluster/members` exists server-side but no client uses it |
| Direct routing to owner    | Client knows hash slots           | Every request hits a node that proxies                      |
| Username + password auth   | `Username`, `Password`, ACL       | Bearer tokens / OIDC / mTLS                                 |
| Connection pooling         | per-host pool with keepalive      | none (every consumer rolls their own)                       |
| Typed errors               | `redis.Nil`, `*redis.Error{Kind}` | parse JSON envelope by hand                                 |
| Cluster client             | `redis.NewClusterClient`          | nonexistent                                                 |

Closing these gaps is the SDK work.

### Where the existing roadmap lands

[ROADMAP.md](../../ROADMAP.md) Phase 5 already scopes "Go client: seed discovery, ring bootstrap, direct owner
hashing, parallel fan-out for QUORUM/ALL". This RFC refines that scope and folds in the auth and
operational-error work surfaced by the example.

## Goals

1. **A `pkg/client` package** that operators reach for instead of hand-rolling HTTP. ~30-line "hello world"
   consumer.
1. **Multi-endpoint high availability without an external LB.** A single node failure must not take down
   consumers. Adding a node must not require redeploying clients.
1. **Username/password authentication as a first-class flow.** Operators with Redis-shop muscle memory should
   hit `client.WithBasicAuth("svc-user", os.Getenv("CACHE_PASSWORD"))` and have it work end-to-end with TLS.
1. **Stable, typed error surface.** `errors.Is(err, client.ErrNotFound)` and
   `errors.As(err, &client.StatusError{Code: "DRAINING"})` are the contract.
1. **All four auth modes (static bearer, basic, mTLS, OIDC) coexist** in one cluster, resolved by the existing
   chain in [`pkg/httpauth/policy.go`](../../pkg/httpauth/policy.go).
1. **The OIDC example collapses to ~30 lines** using the SDK and stays in the tree as the "what the SDK does
   under the hood" reference.

## Non-goals

- **A non-Go SDK.** This RFC is scoped to Go. Other languages will arrive via OpenAPI codegen or hand-rolled
  libraries; the wire protocol changes here (`/v1/auth/login` if we ship it, `/v1/me/can`) become the contract
  for those.
- **Replacing OIDC.** OIDC stays the default for cloud-native / workload-identity deployments.
  Username/password is an additional path, not a replacement.
- **Session state on the server.** Every auth mode resolves to a stateless `Identity{ID, Scopes}` per request.
  We will not introduce server-side sessions even for the username/password flow.
- **Connection pooling beyond `http.Transport` tuning.** The SDK will expose `Transport` knobs; it will not
  invent a connection pool abstraction on top of net/http.
- **Custom on-the-wire protocols.** The SDK speaks the existing REST API. RESP / gRPC / protobuf are
  explicitly out of scope.

## Constraints

- **Backwards compatibility.** Every existing deployment continues to work without config changes. The
  single-endpoint, bearer-token-only story stays valid; new affordances are opt-in.
- **Wire-protocol stability.** Existing `/v1/cache/*`, `/v1/me`, and `/cluster/members` shapes must not break.
  New endpoints (`/v1/auth/login`, `/v1/me/can`) are additive.
- **No new mandatory deps.** Already in `go.mod`: `golang.org/x/oauth2`, `golang.org/x/crypto` (for bcrypt).
  The SDK should not pull in HTTP framework deps (no fiber/gin/etc. — net/http is the contract).
- **The cache's auth chain must remain auditable in a single place.** Today that's
  [`pkg/httpauth/policy.go`](../../pkg/httpauth/policy.go) with one `resolve()` function. Adding Basic must
  not split the chain into N call sites.

## Options

Each section below presents the alternatives we considered. Decisions are deferred to **Recommended path** at
the end of the RFC so each trade-off can be argued in isolation first.

### 5.1 Multi-endpoint discovery and failover

#### Option M1 — Static seeds + failover only

Client takes `[]string` of endpoints at construction. On every request it picks one (round-robin, random, or
least-recent-failure), retries on the next on failure. Membership is fixed for the client's lifetime — nodes
added after deploy are invisible.

- **Pros.** Smallest implementation surface (~150 LOC). Behavior is deterministic and predictable; ops can
  reason about the failure model without reading code. Mirrors `redis.UniversalClient` with static `Addrs`.
- **Cons.** Adding a node requires redeploying every consumer to learn about it. Decommissioning a node
  requires the same. In big deployments this becomes a deployment-coordination headache.

#### Option M2 — Static seeds + periodic topology refresh

Client takes `[]string` of seeds. Periodically (default 30s) it queries `/cluster/members` on any reachable
seed and updates its in-memory view of the cluster. New nodes become eligible without a client redeploy;
removed nodes are dropped after their next failed probe.

- **Pros.** Operators add/remove nodes freely. Existing `/cluster/members` endpoint already serves the data —
  server-side is zero work. Refresh interval is the only new knob. Failure model is still simple: "client sees
  what /cluster/members showed last refresh".
- **Cons.** Periodic background work in the client (one HTTP call every 30s × number of clients). Client
  carries cluster state that can lag behind reality between refreshes. Refresh-storm risk if many clients
  restart simultaneously after a node failure — usually mitigated with jittered intervals.

#### Option M3 — Static seeds + topology refresh + direct-owner routing

M2 plus: client also pulls the consistent-hash ring state and routes each command directly to the key's owner,
skipping the proxy hop.

- **Pros.** Latency-optimal (Phase 5's stated p95 target is "improved vs proxy path"). Cluster-aware clients
  are what Redis/Valkey shops expect.
- **Cons.** Largest implementation surface (~600 LOC for ring bookkeeping + replica fan-out + read-repair
  handling on the client side). Ring state lives in two places (server-side, client-side); membership-version
  drift is a real failure mode. The proxy path on the server stays mandatory for cross-version compatibility
  and non-Go clients, so this is additive rather than load-shifting.

#### Failover sub-decision: which next endpoint

Orthogonal to M1/M2/M3. When the chosen endpoint fails:

- **F1: round-robin.** Walk the seed list in order. Simple, fair.
- **F2: random.** Pick uniformly at random from the still-eligible set. Avoids thundering-herd if many clients
  pick the same primary by configuration order.
- **F3: least-failures-recently.** Track per-endpoint error timestamps; prefer the one whose last failure is
  oldest. Most resilient under partial outages; most state to track.

Recommended sub-default: **F2 random** for the first failover, **F3 last-failures-recently** when we add
metrics — the data is free once we have it.

### 5.2 Username/password auth

#### Option A1 — HTTP Basic on every request

Client sends `Authorization: Basic base64(user:pass)` on every cache call. Server validates against a new
`users:` block in `HYPERCACHE_AUTH_CONFIG`, with passwords stored bcrypted:

```yaml
users:
  - username: svc-billing
    password_bcrypt: $2a$12$...
    identity: svc-billing
    scopes: [read, write]
```

The server's auth chain gains a `resolveBasic` step between bearer and mTLS. The resolved `Identity` shape is
identical to other modes.

- **Pros.** Stateless. Standard HTTP auth — every HTTP client understands it. Matches Redis `AUTH user pass`
  operator mental model exactly. Implementation is ~100 LOC server-side. Requires TLS to be safe (which the
  cache already requires for production).
- **Cons.** Password crosses the wire on every request. If TLS is somehow misconfigured (operator runs without
  it for local dev), every request leaks the password to the network. Mitigation: refuse to honor Basic if
  `c.Protocol() != "https"` _unless_ a new `AllowBasicWithoutTLS: true` opt-in flag is set for dev. Document
  this prominently.

#### Option A2 — Login endpoint that mints a bearer

`POST /v1/auth/login` accepts `{ username, password }`, validates against the same `users:` block, returns
`{ access_token, expires_in }`. Clients use the bearer like any other. Refresh requires re-logging in (no
refresh tokens — that's a server-side session state we said we don't want).

- **Pros.** Password crosses the wire once per token lifetime, not per request. Better fit for environments
  with constrained TLS (rotating client certs every login, etc.). The bearer can be observed/logged without
  leaking the password.
- **Cons.** Two HTTP round-trips to first cache call (login then request). Bearer expiry handling pushes
  complexity into every client. Server has to mint and sign JWTs — needs a key, key rotation strategy, etc.
  Effectively a half-IdP inside the cache. Roughly 300 LOC server-side, plus operational overhead for signing
  key management.

#### Option A3 — Ship both, A1 as default

Server supports both. SDK exposes `WithBasicAuth(user, pass)` (sends Basic) and `WithLoginAuth(user, pass)`
(does the login dance and caches the bearer). Operators pick based on their constraint.

- **Pros.** Operators with strict "password must never traverse the network per-request" requirements have A2;
  everyone else gets the simpler A1.
- **Cons.** Two code paths in both server and SDK. Doubles the documentation surface. Most consumers will pick
  A1 anyway, so we'd be paying for A2's complexity for an edge case.

### 5.3 Where username/password identities live in config

#### Option C1 — New top-level `users:` block

```yaml
tokens:
  - token: bearer-key-...
    identity: svc-A
    scopes: [read]
users:
  - username: svc-billing
    password_bcrypt: ...
    identity: svc-billing
    scopes: [read, write]
cert_identities:
  - cn: svc-mtls
    ...
```

Clear separation by mechanism. Each block is independently parseable.

#### Option C2 — Unified `identities:` block with a `kind` discriminator

```yaml
identities:
  - kind: bearer
    token: ...
    scopes: [read]
  - kind: basic
    username: svc-billing
    password_bcrypt: ...
    scopes: [read, write]
  - kind: cert
    cn: svc-mtls
    scopes: [admin]
```

Single block, polymorphic. Easier to add a new mechanism later.

- **C1 pros**: existing `tokens:` + `cert_identities:` shape stays untouched; we just add one more sibling
  block. Migration is zero.
- **C2 pros**: cleaner future shape; one place to look. But it's a breaking config schema change for everyone
  who's already using `tokens:`/`cert_identities:`.

C1 wins on back-compat alone.

### 5.4 Structured errors on the client

#### Option E1 — Sentinel errors + parsing helper

```go
package client

var (
    ErrNotFound    = ewrap.New("hypercache: key not found")
    ErrUnauthorized = ewrap.New("hypercache: unauthorized")
    ErrDraining    = ewrap.New("hypercache: node draining")
    // ... one per stable `code` from the server's ErrorResponse ...
)

type StatusError struct {
    HTTPStatus int
    Code       string // "NOT_FOUND", "DRAINING", ...
    Message    string
    Details    string
}

func (e *StatusError) Error() string { ... }
func (e *StatusError) Is(target error) bool { ... }  // map known codes to sentinels
```

Consumers use `errors.Is(err, client.ErrNotFound)` for the common path and `errors.As(err, &se)` if they need
the full envelope.

- **Pros.** Idiomatic Go. Composable with `errors.Is`/`errors.As`. Minimal API surface. Stable `code` strings
  stay the wire contract; Go-side sentinels are a thin facade.
- **Cons.** Need to keep the sentinel list in sync with the server's code list. Easy to forget; mitigated by a
  single source-of-truth constant block in both server and SDK.

#### Option E2 — One concrete error type per code

```go
type NotFoundError struct { Key string }
type DrainingError struct { Node string }
```

`errors.As` against the specific type.

- **Pros.** Each error carries its own typed details (the key that was missing, the node that was draining,
  etc.).
- **Cons.** Cartesian product of error types × server codes. Every new code requires a new type + tests.
  Doesn't compose well with generic retry-on-status logic — the consumer has to switch on type rather than
  `errors.Is(err, ErrTransient)`.

E1 strikes the better balance for our scale.

### 5.5 Capability probe

#### Option P1 — Extend `/v1/me` with capabilities

`/v1/me` already returns `{ id, scopes }`. Add a derived `capabilities` field:

```json
{
  "id": "svc-A",
  "scopes": ["read"],
  "capabilities": ["read.cache", "read.me"]
}
```

Clients can introspect at startup. Capability strings stay stable across scope-internal restructuring (if we
ever split "read" into "read.cache" + "read.metrics", scopes change but capability strings stay).

#### Option P2 — `GET /v1/me/can?action=write`

Returns `{ allowed: bool }`. Per-action probe. Cheap to call but needs one request per action.

#### Option P3 — Both — `/v1/me` for "what scopes do I have", `/v1/me/can` for "can I do X right now"

P1 covers the at-startup case; P2 covers cases where the answer might change between requests (scope refresh,
key-scoped ACLs in a future iteration).

For v1: P1 is enough. P2 is cheap enough to add later when key-scoped ACLs become a thing.

## Recommended path

These are recommendations, not decisions. Each section above stands on its own; the choices here are how I'd
argue them given current constraints.

| Decision                  | Recommendation                                                                                                  | Rationale                                                                                                                                                                                         |
| ------------------------- | --------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Multi-endpoint mode       | **M2** as default; **M1** as opt-out flag; **M3** as Phase 5.1 follow-up                                        | M2 nails the "adding a node doesn't require redeploying clients" requirement at minimal cost. M3 is latency-optimal but doubles implementation scope; deferring it keeps the first SDK shippable. |
| Failover policy           | **F2 random** initially, with hooks to swap in **F3** later                                                     | F2 works without any state. F3 is strictly better but needs metrics infrastructure we can add separately.                                                                                         |
| Username/password mode    | **A1 HTTP Basic** as the only mode in v1                                                                        | Stateless, ~100 LOC, matches Redis muscle memory exactly. A2 (login-mint) becomes a follow-up RFC if anyone actually needs it. Most operators won't.                                              |
| Basic-without-TLS posture | **Refuse by default**; honor only if `AllowBasicWithoutTLS: true` is explicitly set in `HYPERCACHE_AUTH_CONFIG` | Fails closed for production; explicit opt-in for `docker-compose up` workflows.                                                                                                                   |
| Config schema             | **C1 new `users:` block**                                                                                       | Zero migration cost. Future polymorphic shape (C2) can come in a later RFC after we have more mechanisms.                                                                                         |
| Error surface             | **E1 sentinels + StatusError**                                                                                  | Idiomatic Go, composes with retry helpers.                                                                                                                                                        |
| Capability probe          | **P1 extend /v1/me** in v1; P2 deferred                                                                         | Enough for the at-startup use case the SDK actually has.                                                                                                                                          |

If we accept the recommended path, the resulting v1 SDK shape is:

```go
package client

// Client speaks the hypercache REST API. Construct via New, dispatch
// commands, close when done.
type Client struct { /* ... */ }

// New constructs a Client. At least one endpoint is required. With no
// auth option set, the client makes anonymous requests — fine for
// dev, will 401 against any production cluster.
func New(endpoints []string, opts ...Option) (*Client, error)

// Endpoints, returns the current view of the cluster (post-refresh).
func (c *Client) Endpoints() []string

// --- options ---

// WithBasicAuth signs every request with HTTP Basic.
func WithBasicAuth(username, password string) Option

// WithBearerAuth signs every request with a static bearer.
func WithBearerAuth(token string) Option

// WithOIDCClientCredentials wraps an oauth2.TokenSource. Token
// refresh is automatic.
func WithOIDCClientCredentials(cfg clientcredentials.Config) Option

// WithTopologyRefresh sets the cluster-membership refresh interval.
// Pass 0 to disable refresh (static seeds only).
func WithTopologyRefresh(interval time.Duration) Option

// WithHTTPClient lets callers inject a pre-configured *http.Client
// (custom transport, retries, traceparent middleware, etc).
func WithHTTPClient(*http.Client) Option

// --- commands ---

// Set, Get, Delete are the obvious bytes-in bytes-out shape.
func (c *Client) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
func (c *Client) Get(ctx context.Context, key string) ([]byte, error)
func (c *Client) Delete(ctx context.Context, key string) error

// GetItem returns the full envelope (version, owners, expiry) for
// callers that need metadata.
func (c *Client) GetItem(ctx context.Context, key string) (*Item, error)

// Identity reports who the client is authenticated as. Useful for
// startup canaries: "is my token actually valid against this cluster?"
func (c *Client) Identity(ctx context.Context) (*Identity, error)

// --- errors ---

var (
    ErrNotFound    = ewrap.New("hypercache: key not found")
    ErrUnauthorized = ewrap.New("hypercache: unauthorized")
    ErrForbidden   = ewrap.New("hypercache: forbidden")
    ErrDraining    = ewrap.New("hypercache: node draining")
)

type StatusError struct {
    HTTPStatus int
    Code       string
    Message    string
    Details    string
}
```

The OIDC example collapses to:

```go
func main() {
    c, err := client.New(
        strings.Fields(os.Getenv("HYPERCACHE_ENDPOINTS")),
        client.WithOIDCClientCredentials(clientcredentials.Config{
            ClientID:     os.Getenv("OIDC_CLIENT_ID"),
            ClientSecret: os.Getenv("OIDC_CLIENT_SECRET"),
            TokenURL:     discoverTokenEndpoint(os.Getenv("OIDC_ISSUER")),
            Scopes:       []string{"openid"},
            EndpointParams: url.Values{"audience": {os.Getenv("OIDC_AUDIENCE")}},
        }),
    )
    if err != nil { ... }
    ctx := context.Background()
    id, _ := c.Identity(ctx)
    fmt.Printf("authed as %s with %v\n", id.ID, id.Scopes)
    c.Set(ctx, "k", []byte("v"), 5*time.Minute)
    v, _ := c.Get(ctx, "k")
    fmt.Println(string(v))
}
```

## Migration

For existing deployments the rollout looks like:

1. **Server side: zero migration required.** Adding `users:` to `HYPERCACHE_AUTH_CONFIG` is opt-in. The
   resolve chain gains a step but unchanged config carries on with the same behavior.
1. **Existing single-endpoint consumers** stay valid: `client.New([]string{"https://cache:8080"}, ...)` is the
   same shape, just with a slice.
1. **The OIDC example** stays in the tree as the SDK's "what's under the hood" reference. The new ~30-line
   variant lives at `__examples/distributed-client/main.go`.
1. **Documentation** — three new pages or sections:
   - [`docs/client-sdk.md`](../client-sdk.md) — SDK reference (when present).
   - Auth section in [`cmd/hypercache-server/README.md`](../../cmd/hypercache-server/README.md) gains the
     `users:` block.
   - [`docs/oncall.md`](../oncall.md#auth-failures) gains a row for "Basic auth failures" mapping to the new
     log lines.

## Open questions

1. **Topology refresh interval default.** 30s is the strawman. Tighter = closer-to-realtime view; looser =
   less network chatter. Should we make it adaptive (Phase 4-style backoff when refreshes return identical
   membership) the way merkle sync now is? Probably yes, but Phase 5.1 — out of scope for v1.
1. **Concurrent refresh deduplication.** Many goroutines calling `c.Set` simultaneously must not each trigger
   a refresh on discovering the same dead peer. Use a singleflight-style guard; detail at implementation.
1. **Basic-auth rate-limiting.** The login-mint variant (A2) trivially rate-limits at the `/v1/auth/login`
   handler. Pure Basic (A1) means the bcrypt check runs on every request — a malicious actor with
   intercepted-but-wrong credentials can DoS via CPU exhaustion. Need per-source-IP token-bucket on Basic
   failures or move to a credential cache (verified user → cached scopes for N seconds). Detail at
   implementation; mention but don't pre-decide here.
1. **mTLS and Basic conflict.** What happens if a client presents both a client cert and an Authorization:
   Basic header? Today the resolve chain order is bearer → mTLS → OIDC. Where does Basic slot in? Suggestion:
   bearer → Basic → mTLS → OIDC, but operators may want it last (so a cert always wins). Could be
   configurable.
1. **Refresh during partition.** If the client's currently-known nodes are all unreachable but a _new_ set is
   alive, the client can't refresh — there's no live endpoint to ask. Mitigation: keep the original seed list
   as a permanent fallback even after refresh replaces the in-memory view. Worth documenting; cheap to
   implement.

## Stopping conditions

- **If A2 (login-mint) gets requested before v1 ships**, reopen the RFC; otherwise A1-only is the v1 target.
- **If M3 (direct-owner routing) becomes a blocker for an existing consumer**, fold it in early — otherwise it
  stays Phase 5.1.
- **If a non-Go SDK becomes urgent**, freeze the wire-protocol decisions in this RFC immediately and reopen a
  separate RFC for the codegen story.
- **If we discover the proxy-path latency is acceptable for all consumers**, deprioritize M3 indefinitely —
  it's a performance optimization, not a correctness fix.
- **If the bcrypt-per-request DoS path (open question 3) can't be mitigated cleanly**, fall back to A2
  (login-mint) for v1 and ship Basic as a follow-up once the rate-limit story is solid.

---

Decisions land in subsequent PRs; this document gets updated with the final disposition once those merge.
