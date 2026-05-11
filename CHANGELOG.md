# Changelog

All notable changes to HyperCache are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Adaptive Merkle anti-entropy scheduling.** New
  [`backend.WithDistMerkleAdaptiveBackoff(maxFactor)`](pkg/backend/dist_memory.go) option lets the auto-sync
  loop double its sleep interval after each tick that finds zero divergence across every peer, capped at
  `maxFactor`. Any tick with at least one dirty peer snaps the factor back to 1× immediately — recovery is
  never lazy. Disabled by default (factor=0 or 1) so existing deployments see no behavior change. Two new
  OTel metrics expose the state: `dist.auto_sync.backoff_factor` (gauge) and `dist.auto_sync.clean_ticks`
  (counter). Each factor change is logged once at Info (`merkle auto-sync backoff factor changed`) — no
  per-tick log spam. Unit tests in
  [`pkg/backend/dist_adaptive_backoff_test.go`](pkg/backend/dist_adaptive_backoff_test.go) cover the ramp,
  the cap, the dirty-tick reset, and the disabled-by-default back-compat invariant.
- **Structured logging for background loops and cluster lifecycle.** HyperCache gained a
  `WithLogger(*slog.Logger)` option ([config.go](config.go)) that wires a structured logger through the
  wrapper. Previously the eviction loop, expiration loop, and HyperCache lifecycle ran fully silent —
  operators had to infer activity from counters. Now: - `eviction loop starting / stopped` with interval,
  max_per_tick, algorithm; per-tick `eviction tick` at Info when items were evicted, Debug when idle
  ([hypercache_eviction.go](hypercache_eviction.go)). - `expiration loop starting / stopped`; per-tick
  `expiration tick` with the same shape ([hypercache_expiration.go](hypercache_expiration.go)). -
  `eviction triggered` on every manual `TriggerEviction` call; debounced/ coalesced triggers log at Debug.
- **DistMemory background-loop startup + cluster-lifecycle logging.** Each loop announces itself with its
  operational knobs so operators can verify configuration from logs alone:
- **AddPeer / RemovePeer log records.** `peer added to membership` and `peer removed from membership` with
  addr, id, and post-mutation member count. Dynamic cluster joins were previously invisible to log-based
  observers. ([pkg/backend/dist_memory.go](pkg/backend/dist_memory.go))
- **Tests pinning the new log contract.** [`hypercache_logging_test.go`](hypercache_logging_test.go) asserts
  the eviction/expiration startup records on a real cache instance with a JSON-captured logger.
  [`tests/integration/dist_logging_test.go`](tests/integration/dist_logging_test.go) asserts the
  cluster-join + AddPeer records against a real DistMemory node. WithLogger(nil) is covered as a documented
  reset-to-discard contract so embedded callers can silence the surface at runtime.

### Wiring

- The `hypercache-server` binary now passes its structured slog.Logger through `WithLogger` so the new lines
  surface in the binary's JSON output alongside the existing dist-transport logs.

- **OIDC verifier on the client API (`Policy.ServerVerify` hook).** When `HYPERCACHE_OIDC_ISSUER` +
  `HYPERCACHE_OIDC_AUDIENCE` are set, the binary fetches the IdP's `/.well-known/openid-configuration` at
  boot, builds a go-oidc-backed JWT verifier, and attaches it to
  [`Policy.ServerVerify`](pkg/httpauth/policy.go). JWTs presented via `Authorization: Bearer <jwt>` are
  validated for signature and `iss`/`aud`/`exp`/`iat`/`nbf`, then the configured identity claim
  (`HYPERCACHE_OIDC_IDENTITY_CLAIM`, default `sub`; common override `email`) becomes `Identity.ID` and the
  configured scope claim (`HYPERCACHE_OIDC_SCOPE_CLAIM`, default `scope` — standard OAuth2 space-separated
  string; arrays also supported) maps to `Identity.Scopes` (only `read`/`write`/`admin` survive; unknown OIDC
  scopes are dropped silently). Coexists with the static-bearer flow: a JWT that doesn't match any configured
  `Tokens` entry falls through `resolveBearer` → `ServerVerify`, so operators can run hybrid deployments
  (machine integrations on static bearers, humans on OIDC). Fail-fast at boot on an unreachable issuer URL or
  partial config (one of issuer/audience set without the other). New dep: `github.com/coreos/go-oidc/v3`
  (justified — JWKS rotation, key id selection, alg validation, claim validation are non-trivial; rolling our
  own is a CVE-magnet). 10 unit tests in [oidc_test.go](cmd/hypercache-server/oidc_test.go) drive an
  in-process IdP stub (signs JWTs with a hand-rolled RSA key, serves discovery + JWKS); 4 integration tests in
  [management_http_test.go](tests/management_http_test.go) verify bearer + OIDC coexistence on the management
  port.
- **`GET /cluster/events` — SSE stream of topology updates.** New Server-Sent Events endpoint on the
  management HTTP port that pushes `members` (full membership snapshot) and `heartbeat` (counters snapshot)
  frames to subscribers, replacing the monitor's 2-second poll cadence with a live stream. Connect-time frames
  carry the current snapshot so a fresh subscriber doesn't wait for the next mutation. The cache wires an
  in-process broadcaster ([`internal/eventbus`](internal/eventbus/bus.go)) that drops events for slow
  consumers (per-subscriber bounded buffer) so the SWIM heartbeat loop never backpressures on a stuck
  operator's browser. Membership state changes propagate via the new
  [`Membership.OnStateChange`](internal/cluster/membership.go) observer hook; heartbeat snapshots tick at 1 Hz
  aligned with the SWIM interval. Read-scope auth (matches existing `/cluster/*` routes); honors the
  management server's lifecycle context for graceful drain on `Stop()`.

### Changed

- **Management server `defaultWriteTimeout` 5 s → 0 (no cap).** fasthttp resets WriteTimeout per response; the
  5 s default force-closed the new SSE stream at exactly that mark, the consumer saw "other side closed", and
  the monitor fell back to polling. Lifting the cap is safe for the mgmt port because it's internal-only and
  JSON handlers complete in milliseconds; idle keep-alive connections are still bounded by the 60 s
  IdleTimeout. Operators who need a write cap can opt in via [`WithMgmtWriteTimeout`](management_http.go).
  Regression-pinned by `TestManagementHTTP_ClusterEvents`, which reads frames for 6 s past the historic
  deadline.
- **Per-route scope enforcement on the management HTTP port.** `WithMgmtControlAuth` is a new option that
  wraps the cluster- mutating control endpoints (`POST /evict`, `POST /clear`, `POST /trigger-expiration`) in
  a stricter auth gate than the observability surface. The hypercache-server binary now wires read-or-better
  on `/stats`/`/config`/`/cluster/*`/`/dist/*` and admin-only on the control routes (see
  `cmd/hypercache-server/ main.go`). `/health` is intentionally NOT auth-wrapped — k8s liveness probes don't
  carry credentials, and a probe failure cascades into a pod-restart loop. Also new: `httpauth.Policy.Verify`,
  the "block-with-error" sibling of `Middleware()` that adapters (like `WithMgmtAuth`/`WithMgmtControlAuth`)
  use when they own their own next-handler dispatch. Existing `Middleware()` is now thin sugar over
  `Verify() + c.Next()` so the auth logic lives in exactly one place.
- **`GET /v1/me` — resolved caller identity.** New scope-protected (`read`) route that reads the resolved
  `httpauth.Identity` from `c.Locals(httpauth.IdentityKey)` and returns `{ id, scopes }` JSON. Mirrors the new
  schema documented at [`/v1/me`](cmd/hypercache-server/openapi.yaml). Unblocks the HyperCache Monitor's Phase
  C2 swap from the legacy `/v1/owners/__probe__` probe to a real introspection of the bound bearer token's
  grants. Anonymous mode (`AllowAnonymous: true`) returns `id: "anonymous"` with all three scopes — same
  identity the policy emits internally. Drift test
  ([`openapi_test.go`](cmd/hypercache-server/openapi_test.go)) and auth-coverage table
  ([`auth_test.go`](cmd/hypercache-server/auth_test.go)) updated in lockstep.
- **Client API auth v2: multi-token, scoped, mTLS-capable.** New [`pkg/httpauth/`](pkg/httpauth/) package with
  `Policy`, `TokenIdentity`, `CertIdentity`, `Scope` types and a scope-enforcing fiber middleware. Replaces
  the single-token bearerAuth helper in `cmd/hypercache-server/main.go`. Three credential classes resolved in
  priority order (bearer → mTLS cert → ServerVerify hook), with constant-time multi-token compare that visits
  every configured token even on early match to prevent token-cardinality timing leaks. Per-route scope
  enforcement: `GET`/`HEAD`/owners-lookup/`batch-get` require `ScopeRead`;
  `PUT`/`DELETE`/`batch-put`/`batch-delete` require `ScopeWrite`. Anonymous identity (with
  `AllowAnonymous: true`) receives all scopes — used by the binary to preserve the zero-config dev posture.
- **YAML auth config + legacy env-var coexistence.** `HYPERCACHE_AUTH_CONFIG=/etc/hypercache/auth.yaml` (new)
  loads a multi-token policy with per-identity scopes:

  ```yaml
  tokens:
    - id: app-prod
      token: "<secret>"
      scopes: [read, write]
    - id: ops
      token: "<secret>"
      scopes: [admin]
  cert_identities:
    - subject_cn: app.internal
      scopes: [read]
  allow_anonymous: false
  ```

  The legacy `HYPERCACHE_AUTH_TOKEN` keeps working byte-identical: one synthesized identity with all three
  scopes. The two env vars are NOT mutually exclusive — `HYPERCACHE_AUTH_CONFIG` governs the client API,
  `HYPERCACHE_AUTH_TOKEN` continues to drive the dist transport's symmetric peer auth (single trust domain).
  Both can be set in the same deployment without conflict. Missing or malformed config files exit the binary
  non-zero rather than fall through to permissive open mode — fail-closed by design.

- **mTLS on the client API.** New env vars `HYPERCACHE_API_TLS_CERT`, `HYPERCACHE_API_TLS_KEY`, and
  `HYPERCACHE_API_TLS_CLIENT_CA` wrap the listener with `tls.NewListener`. With CA set,
  `RequireAndVerifyClientCert` is enabled and the verified peer cert's Subject CN is matched against the
  policy's `CertIdentities` to resolve the calling identity. Plaintext, standard-TLS, and mTLS shapes all
  share one listener path. End-to-end coverage at
  [cmd/hypercache-server/mtls_e2e_test.go](cmd/hypercache-server/mtls_e2e_test.go) drives a real handshake
  against an in-process CA / server-cert / client-cert chain and asserts CN-to-identity resolution works in
  both directions (matching CN → 200, non-matching CN → 401).

### Fixed

- **`TestDistRebalanceReplicaDiffThrottle` no longer flakes under `make test-race`.** The test's 900ms hard
  sleep wasn't enough wall-clock budget for the rebalancer's 80ms-tick loop to actually fire 11 ticks under
  `-race` + `-shuffle=on`'s scheduler pressure. Replaced the sleep with a 5-second polling loop that exits as
  soon as the throttle metric increments — happy path stays fast, slow runners get headroom. Same shape as the
  cache's other timing-sensitive integration tests.
  ([tests/integration/dist_rebalance_replica_diff_throttle_test.go](tests/integration/dist_rebalance_replica_diff_throttle_test.go))

### Security

- **Constant-time bearer-token compare on the client API.** Replaced the plaintext `got != want` check at
  [cmd/hypercache-server/main.go](cmd/hypercache-server/main.go) with `crypto/subtle.ConstantTimeCompare` to
  defeat timing side-channels. A naive string compare returns as soon as the first differing byte is found,
  leaking per-byte equality of `HYPERCACHE_AUTH_TOKEN` to a remote attacker who can measure response time. The
  fix mirrors the dist transport's existing constant-time check at
  [pkg/backend/dist_http_server.go:144-152](pkg/backend/dist_http_server.go#L144-L152). No public API change;
  the env-var contract and "empty token → open mode" back-compatible behavior are unchanged. New auth-test
  suite at [cmd/hypercache-server/auth_test.go](cmd/hypercache-server/auth_test.go) pins the contract:
  missing/wrong/malformed/lowercase/wrong-length bearer headers all return 401, public meta routes
  (`/healthz`, `/v1/openapi.yaml`) stay reachable without credentials, every protected route fires the
  wrapper. The new `newAuthedServer` helper drives `registerClientRoutes` directly so future wiring
  regressions are caught (the existing `handlers_test.go::newTestServer` deliberately bypasses auth for
  handler-correctness coverage).

### Added

- **OpenAPI 3.1 specification + drift-detection.** The `hypercache-server` binary now embeds its own contract
  via [`cmd/hypercache-server/openapi.yaml`](cmd/hypercache-server/openapi.yaml) (`//go:embed`) and serves it
  at `GET /v1/openapi.yaml` — every running node is self-describing. The spec covers all nine client routes
  (single-key PUT/GET/HEAD/DELETE, owners lookup, three batch operations, plus the `/healthz` and
  `/v1/openapi.yaml` meta endpoints), with reusable `ErrorResponse`, `ItemEnvelope`, and batch-operation
  schemas, the `bearerAuth` security scheme, and `operationId` on every operation for codegen-friendliness. A
  drift detector at [cmd/hypercache-server/openapi_test.go](cmd/hypercache-server/openapi_test.go) drives
  `registerClientRoutes` directly and asserts every fiber-registered route has a matching path in the spec —
  and vice-versa — so the contract cannot silently fall out of sync with the binary. Two CI workflows back
  this up at [.github/workflows/openapi.yml](.github/workflows/openapi.yml): `redocly lint` validates the
  schema against the OpenAPI 3.1 meta-spec, and the Go drift test runs on every change to `main.go` or the
  spec. The docs site renders the same spec inline at the new [API Reference](docs/api.md) page via the
  `mkdocs-swagger-ui-tag` plugin — a single source of truth for the binary, the docs, and any client codegen
  that points at a live cluster.
- **Documentation site on GitHub Pages**, built with MkDocs Material and published automatically on every push
  to `main`. Eight navigated pages — landing, quickstart, 5-node cluster tutorial, Helm chart guide,
  server-binary reference, distributed-backend architecture, operations runbook, RFC index — plus the
  CHANGELOG and the `cmd/hypercache-server/README.md` pulled in via the include-markdown plugin so they don't
  drift. A build-time hook at [`_mkdocs/hooks.py`](_mkdocs/hooks.py) rewrites repo-relative source-code
  references (`../pkg/foo.go`) into canonical GitHub URLs so the same markdown renders correctly both on
  github.com and on the rendered Pages site. Workflow at
  [`.github/workflows/docs.yml`](.github/workflows/docs.yml) builds with `--strict` on every PR (catches
  broken docs-internal links on submission) and deploys via `actions/deploy-pages@v4` on main pushes. The
  README now links to the rendered site. Polishing pass on the existing markdown surface: relaxed `mdl` rules
  that fight MkDocs/frontmatter idioms (MD041 for YAML frontmatter pages, MD010 for Go's tab-in-code-blocks
  convention, MD033/MD032 for Material's grid-cards HTML).
- **Richer client API — metadata inspection, JSON envelopes, batch operations.** Three additions to the
  `cmd/hypercache-server` HTTP surface: - `HEAD /v1/cache/:key` returns the value's metadata in `X-Cache-*`
  response headers (Version, Origin, Last-Updated, TTL-Ms, Expires-At, Owners, Node) with no body — fast
  existence + TTL inspection without paying the value-transfer cost. 200 if present, 404 if not. -
  `GET /v1/cache/:key` now honors `Accept: application/json` and returns an `itemEnvelope` with the same
  metadata as HEAD plus the base64-encoded value. The bare-`curl` default remains raw bytes via
  `application/octet-stream` — current clients are unaffected. - `POST /v1/cache/batch/{get,put,delete}`
  enable bulk operations in a single round-trip. Each request carries an array; the response carries one
  result entry per item with per-item status, owners, and error reporting. `batch-put` items accept either
  UTF-8 strings (default) or base64-encoded byte payloads via `value_encoding: "base64"`. Per-item errors are
  surfaced in `error` + `code` fields without failing the whole batch. Six unit tests at
  [cmd/hypercache-server/handlers_test.go](cmd/hypercache-server/handlers_test.go) pin the contracts: HEAD
  present/missing, Accept-JSON envelope shape, default-raw round-trip, mixed-encoding batch-put, batch-get
  found/missing, batch-delete cycle.
- **SWIM self-refutation + cross-process gossip dissemination.** Closes the last `experimental` marker on the
  heartbeat path. Three pieces: - **`acceptGossip` self-refute** — incoming entries that reference the local
  node as Suspect or Dead at incarnation ≥ ours now bump the local incarnation and re-mark Alive.
  Higher-incarnation-wins propagation in the same function disseminates the refutation cluster-wide, so a
  falsely- suspected node can clear suspicion through gossip alone (pre-fix the only path was a fresh
  probe). - **HTTP gossip wire** — new `Gossip(ctx, targetID, members)` method on `DistTransport`, new
  `POST /internal/gossip` server endpoint (auth-wrapped), new `GossipMember` wire DTO. `runGossipTick` now
  falls through to the HTTP path when the transport isn't an `InProcessTransport`, so cross-process clusters
  disseminate membership state — pre-Phase-E this was an in-process-only no-op. - The `experimental` qualifier
  is removed from `heartbeatLoop`'s comment + the heartbeat-section field doc; SWIM-style indirect probes
  (Phase B.1) and self-refutation (this round) together provide the SWIM properties the marker was tracking.
  Regression coverage at
  [tests/integration/dist_swim_refute_test.go](tests/integration/dist_swim_refute_test.go):
  `TestDistSWIM_HTTPGossipExchange` exercises the wire (A pushes membership to B over HTTP; B's view
  converges), `TestDistSWIM_SelfRefute` drives a forged "you are suspect" gossip into a node's
  `/internal/gossip` and asserts the local incarnation bumps + state returns to Alive.
- **End-to-end resilience test** at
  [scripts/tests/20-test-cluster-resilience.sh](scripts/tests/20-test-cluster-resilience.sh) — kills a docker
  container mid-run, asserts the surviving 4 nodes still serve every previously-written key AND every key
  written during the outage, then restarts the killed node and asserts it converges on the full state within
  60 s. Validates Phase B.2 (hint-replay) and the post-restart anti-entropy paths against the _actual_ docker
  network — a class of bugs in-process tests can't reach. 24 assertions across 6 phases. Wired into both
  `make test-cluster` (runs after the smoke, exit-code-propagated through the same teardown trap) and the
  `cluster` CI workflow as a follow-up step.
- **Cross-process cluster smoke in CI** — [.github/workflows/cluster.yml](.github/workflows/cluster.yml) boots
  the 5-node `docker-compose.cluster.yml` stack on every PR/push, waits for `/healthz` on every node, then
  runs the assertion script at [scripts/tests/10-test-cluster-api.sh](scripts/tests/10-test-cluster-api.sh).
  Container logs are dumped on failure for debuggability without a re-run. This catches the class of bugs that
  escaped the previous PR (factory dropped DistMemoryOptions, seeds without IDs, json.RawMessage on non-owner
  GET) — none would have been detected by unit/integration tests because they only exercised in-process
  behavior.
- **`make test-cluster` Makefile target** mirrors the CI flow for local development: brings the cluster up,
  waits, runs the smoke, and tears down on the way out (preserving the smoke's exit code).
- **`scripts/tests/wait-for-cluster.sh`** is the polling helper that blocks until every node's `/healthz`
  returns 200, with a default 30-second deadline configurable via `TIMEOUT_SECS`. Used by both the Makefile
  and the CI workflow so the assertion script downstream never races the listener bind.
- **`scripts/tests/10-test-cluster-api.sh` hardened** from a print-only smoke into a real regression test: 17
  explicit assertions across propagation / wire-encoding / cross-node delete, color-coded `OK`/`FAIL` output,
  exit code reflects total failure count.
- **`cmd/hypercache-server/main_test.go`** — fast Go unit tests pinning the wire-encoding contracts on
  `writeValue` / `decodeBase64Bytes`. Covers `[]byte` (writer path), `string` (replica path),
  `json.RawMessage` (non-owner-GET path), and the base64-heuristic length floors. Runs without docker for
  tight feedback during development.
- **GitHub Release automation** — [.github/workflows/release.yml](.github/workflows/release.yml) triggers on
  `v*.*.*` tag pushes and creates the GitHub Release page via `softprops/action-gh-release@v2`. The release
  body pins readers to the matching container image tag in GHCR and the CHANGELOG.md at that ref;
  PR-since-previous-tag notes are appended automatically. Pre-release tags (`v1.2.3-rc1`, `v1.2.3-beta`) are
  flagged via the `prerelease` field; `workflow_dispatch` lets operators (re-)create a release for an existing
  tag without re-tagging.
- **Helm chart for k8s deployment** at [chart/hypercache/](chart/hypercache). Renders into a StatefulSet
  (stable per-pod hostnames so the `id@addr` seed list resolves deterministically), a headless Service for
  peer DNS, separate client and management Services, an optional chart-managed Secret for the auth token (or
  external Secret reference for production rotation), a PodDisruptionBudget (default `minAvailable: 4`), pod
  anti-affinity, and a hardened pod security context (non-root, read-only rootfs, all caps dropped). The
  ServiceAccount + Service + StatefulSet composition matches what `helm install` emits via `helm lint` and
  `helm template` against any kube-version. Configure cluster size, replication factor, capacity, heartbeat,
  hint TTL, rebalance interval, and resources via standard Helm values — see
  [chart/hypercache/values.yaml](chart/hypercache/values.yaml) for the full surface.
- **Pre-commit excludes Helm templates** from `check-yaml` and `yamllint`. Both validators choke on
  Go-template `{{ ... }}` syntax inside the chart manifests; `helm lint` is the right validator for those, and
  CI runs that separately.
- **Multi-arch container image workflow** — [.github/workflows/image.yml](.github/workflows/image.yml) builds
  the `hypercache-server` Docker image for `linux/amd64` and `linux/arm64` via buildx + QEMU, publishing to
  GHCR (`ghcr.io/<owner>/<repo>/hypercache-server`). PR triggers build-only (no registry pollution), `main`
  pushes publish `:main` and `:sha-<short>`, semver tag pushes (`v*.*.*`) publish `:v1.2.3`, `:1.2.3`, `:1.2`,
  `:1`, and `:latest`. `:latest` is **deliberately restricted to semver tag pushes** — production deployments
  pinning `:latest` always get a stable release, never an in-flight `main` commit. GHA cache speeds re-builds
  when only Go source has changed.

### Fixed

- **Cluster propagation was completely broken.** The `DistMemoryBackendConstructor.Create` factory in
  `factory.go` silently discarded `cfg.DistMemoryOptions` and called `backend.NewDistMemory(ctx)` with **no
  arguments**. Every `WithDistNode`, `WithDistSeeds`, `WithDistReplication`, etc. that callers wired through
  `hypercache.NewConfig` was a silent no-op, leaving every node with a default standalone configuration that
  only knew itself. The factory now forwards `cfg.DistMemoryOptions...` like every other backend constructor
  does. This was the production-blocking bug — a Set on one node never reached its peers because the other
  nodes weren't actually in any node's ring.
- **Seed addresses without node IDs produced a broken ring.** `initStandaloneMembership` added every seed to
  membership with an empty `NodeID`, so the consistent-hash ring was built over empty-string owners. `Set`
  would resolve owners as `["", "", "self"]`, fan-outs to `""` failed with `ErrBackendNotFound`, the writer
  self-promoted, and the data never reached its peers. The HTTP transport has no node-discovery protocol, so
  the only way to populate node IDs in the ring is at configuration time. Seeds now accept an optional
  `id@addr` syntax (`node-2@hypercache-2:7946`) — bare `addr` keeps the legacy empty-ID behavior for
  in-process tests. Production deployments must use `id@addr`.
- **`Remove` from a non-primary owner skipped the primary.** `removeImpl` checked `dm.ownsKeyInternal(key)`
  (true for any ring owner) and ran `applyRemove` locally — but `applyRemove`'s fan-out only covers
  `owners[1:]` under the assumption the caller is `owners[0]`. When a replica initiated the remove, the
  primary never got the delete. The Remove path now mirrors Set: non-primary callers forward to the primary,
  primary applies + fans out. Tombstones now propagate cluster-wide regardless of which node receives the
  DELETE.
- **Client API responses were unhelpful.** Set/Remove returned `204 No Content` with empty bodies; errors were
  raw text via `SendString`. Replaced with structured JSON: PUT/DELETE return
  `{key, stored|deleted, bytes, node, owners}` so operators can immediately see where the value landed; errors
  return `{error, code}` with stable code strings (`BAD_REQUEST`, `NOT_FOUND`, `DRAINING`, `INTERNAL`). Added
  `GET /v1/owners/:key` for client-side ring visibility.
- **GET response leaked base64 on replicas.** `[]byte` values round-trip through JSON as base64 strings;
  replica nodes that received a value via the dist HTTP transport stored it as a `string` and returned it raw,
  so a `PUT world` on node-A resulted in `d29ybGQ=` from `GET` on node-B. The client GET handler now
  base64-decodes string values when they look like valid byte content, restoring writer-receiver symmetry.
- **GET on non-owner nodes returned a JSON-quoted base64 string.** The dist HTTP transport's `decodeGetBody`
  decodes `Item.Value` as `json.RawMessage` to preserve wire-bytes type fidelity. The client GET handler's
  type switch only matched `[]byte` and `string`, so non-owner GETs (which always go through the forward-fetch
  path) fell to the `default` branch and re-emitted the value as JSON — producing `"d29ybGQ="` instead of
  `world`. Added an explicit `json.RawMessage` case that interprets the raw JSON as a string when possible,
  then base64-decodes if applicable. Verified end-to-end against the 5-node Docker cluster where two of the
  five nodes are non-owners for any given key.

- **Race in `queueHint` between hint enqueue and hint replay.** Pre-fix, the metric write
  `dm.metrics.hintedBytes.Store(dm.hintBytes)` happened _after_ releasing `hintsMu`, so a concurrent
  `adjustHintAccounting` call from the replay loop could race the read. Capturing the value under the lock
  closes the race. Surfaced when migration failures began funneling through `queueHint` (Phase B.2 below) —
  previously the migration path swallowed errors silently, so the hint enqueue rate from rebalance ticks was
  much lower.

### Added (earlier in this cycle)

- **Structured logging on the dist backend.** New `WithDistLogger(*slog.Logger)` option wires a structured
  logger into the dist backend's background loops (heartbeat, hint replay, rebalance, merkle sync) and
  operational error surfaces (HTTP listener bind failures, serve-goroutine exits, failed migrations during
  rebalance, dropped hints, peer state transitions). Library default is silent — `WithDistLogger` not called
  installs a `slog.DiscardHandler` so the dist backend never writes to stderr unless the caller opts in. Every
  record is pre-bound with `component=dist_memory` and `node_id=<id>` attributes for grep/filter. Phase A.1 of
  the production-readiness work.
- **OpenTelemetry tracing on the dist backend.** New `WithDistTracerProvider(trace.TracerProvider)` option
  opens spans on every public `Get` / `Set` / `Remove`, with child spans (`dist.replicate.set` /
  `dist.replicate.remove`) per peer during fan-out. Span attributes include `cache.key.length`,
  `dist.consistency`, `dist.owners.count`, `dist.acks`, `cache.hit`, and `peer.id`. Cache key _values_ are
  intentionally never recorded on spans — keys can be PII (user IDs, session tokens). Library default is a
  no-op tracer (`noop.NewTracerProvider`), so spans cost nothing unless the caller opts in. New
  `ConsistencyLevel.String()` method renders consistency levels human-readably for log/span attrs. Phase A.2
  of the production-readiness work.
- **OpenTelemetry metrics on the dist backend.** New `WithDistMeterProvider(metric.MeterProvider)` option
  registers an observable instrument for every field on `DistMetrics` — counters for cumulative totals
  (`dist.write.attempts`, `dist.forward.*`, `dist.hinted.*`, `dist.merkle.syncs`, `dist.rebalance.*`, etc.),
  gauges for current state (`dist.members.alive`, `dist.tombstones.active`, `dist.hinted.bytes`,
  last-operation latencies in nanoseconds, etc.). A single registered callback observes all instruments from
  one `Metrics()` snapshot per collection cycle, so there is no per-operation overhead beyond the existing
  atomic counters. Names use the `dist.` prefix so a Prometheus exporter renders them under a single
  subsystem. `Stop` unregisters the callback so the SDK does not invoke it against a stopped backend. Library
  default is a no-op meter, so metrics cost nothing unless the caller opts in. Phase A.3 of the
  production-readiness work.
- **SWIM-style indirect heartbeat probes.** New `WithDistIndirectProbes(k, timeout)` option enables the
  indirect- probe refutation path: when a direct heartbeat to a peer fails, this node asks `k` random alive
  peers to probe the target on its behalf, and only marks the target suspect if every relay also fails.
  Filters caller-side network blips (transient NIC reset, single stuck connection in this node's pool) that
  would otherwise cause spurious suspect/dead transitions. New transport method
  `IndirectHealth(ctx, relayNodeID, targetNodeID)` and HTTP endpoint `GET /internal/probe?target=<id>` carry
  the probe; auth-wrapped identically to the rest of `/internal/*`. New metrics
  `dist.heartbeat.indirect_probe.success`, `.failure`, `.refuted` expose probe outcomes. `k = 0` (default)
  preserves the pre-Phase-B behavior. Phase B.1 of the production-readiness work — note that the heartbeat
  path still carries the `experimental` marker until self-refutation via incarnation-disseminating gossip
  lands in a later phase.
- **Migration failures now retry through the hint queue.** When a rebalance forwards a key to its new primary
  and the transport returns _any_ error (not just `ErrBackendNotFound`), the item is enqueued onto the
  existing hint-replay queue keyed by the new primary, instead of being logged and dropped. The hint-replay
  loop drains it on its configured schedule until the hint TTL expires. Same broadening applies to the
  `replicateTo` fan-out on the primary `Set` path — transient HTTP failures (timeout, 5xx, connection reset)
  no longer silently drop replicas. Phase B.2 of the production-readiness work.
- **On-wire compression for the dist HTTP transport.** New `DistHTTPLimits.CompressionThreshold` field opts
  the auto-created HTTP client into gzip-compressing Set request bodies whose serialized payload exceeds the
  configured byte threshold. The client sets `Content-Encoding: gzip` and the server transparently
  decompresses (via fiber v3's auto-decoding `Body()`). Threshold `0` (default) preserves the pre-Phase-B wire
  format byte-for-byte. Operators on bandwidth-constrained links with values above ~1 KiB typically see
  meaningful reductions; below-threshold values pay no compression cost. Roll out the threshold to all peers
  before raising it on any peer — a server with compression disabled will reject a gzip body with HTTP 400.
  Phase B.3 of the production-readiness work.
- **Drain endpoint for graceful shutdown.** New `DistMemory.Drain(ctx)` method and `POST /dist/drain` HTTP
  endpoint mark the node for shutdown: `/health` returns 503 so load balancers stop routing, `Set`/`Remove`
  return `sentinel.ErrDraining`, `Get` continues to serve so in-flight reads complete. New `IsDraining()`
  accessor for dashboards. New metric `dist.drains` records transitions. Drain is one-way and idempotent.
  Phase C.1 of the production-readiness work.
- **Cursor-based key enumeration** replaces the pre-Phase-C testing-only `/internal/keys` endpoint. The
  endpoint now returns shard-level pages with a `next_cursor` token; clients walk the cursor chain to
  enumerate the full key set. New `?limit=<n>` query parameter truncates within a shard for clusters with very
  large shards (response then carries `truncated=true` and the same `next_cursor`). The
  `DistHTTPTransport.ListKeys` helper now walks pages internally so existing callers (anti-entropy fallback,
  tests) keep their full-set semantics unchanged. Phase C.2 of the production-readiness work.
- **Operations runbook** at [docs/operations.md](docs/operations.md) covering split-brain, hint-queue
  overflow, rebalance under load, replica loss, observability wiring (logger/tracer/meter), drain procedure,
  and capacity-planning notes. Cross-links each failure mode to the metrics that surface it. Phase C.3 of the
  production-readiness work.
- **Production server binary** at [`cmd/hypercache-server`](cmd/hypercache-server). Wraps DistMemory via
  HyperCache and exposes three HTTP listeners per node: the client REST API
  (`PUT`/`GET`/`DELETE /v1/cache/:key`), management HTTP (`/health`, `/stats`, `/config`, `/dist/metrics`,
  `/cluster/*`), and the inter-node dist HTTP. 12-factor configuration via `HYPERCACHE_*` environment
  variables — same binary runs in Docker, k8s, and bare-metal. Graceful shutdown on SIGTERM/SIGINT runs Drain
  → API stop → HyperCache Stop with a 30 s deadline. JSON-formatted slog logger pre-bound with `node_id`.
  Multi-stage `Dockerfile` builds a distroless static image (`gcr.io/distroless/static-debian12:nonroot`).
- **5-node local cluster compose** at [`docker-compose.cluster.yml`](docker-compose.cluster.yml) — five
  hypercache-server nodes on a shared `hypercache-cluster` Docker network, each knowing the other four as
  seeds, replication=3. Client APIs exposed on host ports 8081–8085, management HTTP on 9081–9085. Includes a
  smoke-test recipe in the [server README](cmd/hypercache-server/README.md). Phase D of the
  production-readiness work.
- **`HyperCache.DistDrain(ctx)`** convenience method in [hypercache_dist.go](hypercache_dist.go) — calls Drain
  on the underlying DistMemory backend when one is configured, no-op on in-memory / Redis backends. Lets the
  server binary trigger drain without type-asserting through the unexported backend field.

## [0.5.0] — 2026-05-05

### Security

- **Fixed silent inbound auth bypass when `DistHTTPAuth.ClientSign` was set without a matching inbound
  verifier.** Previously, a config of `DistHTTPAuth{ClientSign: hmacSign}` flipped the internal `configured`
  predicate to true (causing the auto-client to sign outbound traffic), but `verify()` had no inbound material
  and silently allowed every request — so an operator wiring half of an HMAC scheme could end up with
  signed-out / open-in nodes that looked authenticated. The internal predicate is now split into
  `inboundConfigured()` / outbound-path checks, and `NewDistMemory` rejects this shape at construction with
  `sentinel.ErrInsecureAuthConfig`. Operators who legitimately want signed-out / open-in deployments (e.g.
  inbound is gated by an L4 firewall or service mesh below this server) must opt in via the new
  `DistHTTPAuth.AllowAnonymousInbound` field. All other configurations (`Token`-only, `Token+ServerVerify`,
  `Token+ClientSign`, `ServerVerify`-only) are unaffected. Reported by the post-tag security review; addressed
  before any v0.5.0 public announcement.

### Added

- `DistHTTPAuth.AllowAnonymousInbound` — explicit opt-in for asymmetric signed-out / open-in configurations.
- `sentinel.ErrInsecureAuthConfig` — surfaced from `NewDistMemory` when the auth policy would silently disable
  inbound enforcement.

## [0.4.3] — 2026-05-04

A modernization release. The headline themes:

- Eviction is now sharded by default for concurrency-friendly throughput.
- The distributed-memory backend (`DistMemory`) gained body limits, TLS, bearer-token auth, lifecycle-context
  cancellation, and surfaced listener errors.
- A typed wrapper (`Typed[T, V]`) is available for compile-time type-safe access without the caller-side type
  assertions of the untyped API.
- The legacy `pkg/cache` v1 store and the `longbridgeapp/assert` test dependency are gone.

The full course-correction plan (Phase 0 baseline → Phase 6 file split, plus Phase 5a–5e DistMemory hardening)
is in commit history. The two RFCs that informed the design decisions live under [docs/rfcs/](docs/rfcs/).

### Breaking changes

- **`pkg/cache` v1 removed.** All callers must use `pkg/cache/v2`.
- **`longbridgeapp/assert` test dependency removed.** Tests now use `stretchr/testify/require`. Internal test
  code only — no impact on library consumers, but downstream contributors authoring tests against this
  codebase must use `require`.
- **`sentinel.ErrMgmtHTTPShutdownTimeout` removed.** `ManagementHTTPServer.Shutdown` now calls
  `app.ShutdownWithContext` and returns the underlying ctx error directly. Callers comparing against the
  removed sentinel must switch to `errors.Is(err, context.DeadlineExceeded)` or equivalent.
- **Sharded eviction is default-on (32 shards).** Items no longer evict in strict global LRU/LFU order — the
  algorithm operates independently within each shard. Total capacity is honored within ±32 (one slot of slack
  per shard). Use `WithEvictionShardCount(1)` to restore strict-global ordering at the cost of single-mutex
  contention.
- **`hypercache.go` decomposed into 6 files** (`hypercache.go`, `hypercache_io.go`, `hypercache_eviction.go`,
  `hypercache_expiration.go`, `hypercache_dist.go`, `hypercache_construct.go`). No public API change;
  third-party patches against line numbers in the prior single-file layout will not apply.
- **`ManagementHTTPServer` constructor order fix.** `WithMgmtReadTimeout` and `WithMgmtWriteTimeout`
  previously mutated struct fields _after_ `fiber.New` had locked in the defaults — the options were silent
  no-ops. Construction order is now correct, so any code relying on the silent no-op (e.g., setting absurd
  values knowing they would be ignored) will see those values take effect.

### Performance

Measurements on Apple M4 Pro, `go test -bench`, `count=5`, benchstat.

- **Per-shard atomic `Count`.** `BenchmarkConcurrentMap_Count`: 53 → ~10 ns/op. `_CountParallel`: 1181 → ~13
  ns/op. Eliminates the lock-storm that previously serialized on a single mutex during eviction-loop count
  checks.
- **Sharded eviction algorithm** (`pkg/eviction/sharded.go`). Replaces the global eviction-algorithm mutex
  with 32 per-shard mutexes routed by the same hash `ConcurrentMap` uses, so a key's data shard and eviction
  shard align (cache-locality on Set).
- **`iter.Seq2` migration** replacing channel-based `IterBuffered`. `BenchmarkConcurrentMap_All` (renamed from
  `_IterBuffered`): 757µs → 26.5µs/op (-96.51%). Bytes/op: 1.73 MiB → 0 B/op. Allocs/op: 230 → 0. Eliminated
  32 goroutines + 32 channels per iteration.
- **xxhash consolidation** (`pkg/cache/v2/hash.go`). Replaced inlined FNV-1a with `xxhash.Sum64String` folded
  to 32 bits. `BenchmarkConcurrentMap_GetShard`: 10.07 → 3.46 ns/op (-65.63%).
- **Sharded item-aware eviction was tried and rejected** per
  [RFC 0001](docs/rfcs/0001-backend-owned-eviction.md). The hypothesis (duplicate-map overhead is the
  bottleneck) was falsified — sharded contention dominates. Code removed; lessons preserved in the RFC for
  future contributors.

### Features

- **`hypercache.Typed[T, V]` wrapper** for compile-time type-safe cache access. Wraps an existing
  `HyperCache[T]`; multiple `Typed` views can share one underlying cache over disjoint keyspaces. Includes
  `Set`, `Get`, `GetTyped` (explicit `ErrTypeMismatch`), `GetWithInfo`, `GetOrSet`, `GetMultiple`, `Remove`,
  `Clear`. See [hypercache_typed.go](hypercache_typed.go) and
  [RFC 0002 Phase 1](docs/rfcs/0002-generic-item-typing.md). Phase 2 (deep `Item[V]` generics) is v3
  territory, conditional on adoption signal.
- **`WithDistHTTPLimits(DistHTTPLimits)` option** for the dist transport: server `BodyLimit` / `ReadTimeout` /
  `WriteTimeout` / `IdleTimeout` / `Concurrency`, plus client `ResponseLimit` / `ClientTimeout`. Defaults: 16
  MiB request/response body cap, 5 s read/write/client timeout, 60 s idle, fiber's 256 KiB concurrency cap.
  Partial overrides honored — zero fields inherit defaults.
- **`WithDistHTTPAuth(DistHTTPAuth)` option** for bearer-token auth on `/internal/*` and `/health` (`Token`
  for the common case; `ServerVerify`/`ClientSign` hooks for JWT, mTLS-derived identity, HMAC, etc.).
  Constant-time token compare on the server side. The auto-created HTTP client signs every outgoing request
  with the same token. Mismatched-token peers are rejected with HTTP 401 (`sentinel.ErrUnauthorized`).
- **TLS support** via `DistHTTPLimits.TLSConfig`. The server wraps its listener with `tls.NewListener`; the
  auto-created HTTP client attaches the same `*tls.Config` to its `Transport.TLSClientConfig` with ALPN forced
  to `http/1.1` (fiber/fasthttp doesn't speak h2). Same `*tls.Config` configures both sides — operators
  applying it consistently across the cluster get encrypted intra-cluster traffic out of the box. Plaintext
  peers handshake-fail.
- **Dist server lifecycle context** — `DistMemory.LifecycleContext()` exposes a context derived from the
  constructor's that is canceled on `Stop()`. Replaces the prior pattern where handlers captured the
  constructor's `context.Background()` and never observed cancellation. In-flight handlers and replica
  forwards see `Done()` the moment `Stop` is called.
- **`LastServeError()` accessor** on both `distHTTPServer` and `ManagementHTTPServer`. Replaces the prior
  `_ = serveErr` pattern that silently swallowed listener-loop crashes — operators can now surface the failure
  to logs/alerts.
- **`Stop()` goroutine-leak fix.** Both `distHTTPServer.stop` and `ManagementHTTPServer.Shutdown` now call
  `app.ShutdownWithContext(ctx)` directly instead of wrapping `app.Shutdown()` in a goroutine and racing it
  against ctx done (which leaked the goroutine when ctx fired first).
- **New sentinels:** `sentinel.ErrTypeMismatch`, `sentinel.ErrUnauthorized`.

### Internal

Worth surfacing for contributors:

- **v2 module layout** is the file split listed under "Breaking changes" above — readability win, no API
  change.
- **Test helpers** introduced under `tests/`: `tests/dist_cluster_helper.go::SetupInProcessCluster[RF]`,
  `tests/merkle_node_helper.go`, `pkg/backend/dist_memory_test_helpers.go::EnableHTTPForTest` (build tag
  `test`).
- **Lint discipline:** 35 `nolint` directives total across the repo, each with a one-line justification.
  golangci-lint v2.12.2 runs clean with `--build-tags test`.

### Removed

- `pkg/cache` v1 (see "Breaking changes").
- `longbridgeapp/assert` test dependency (see "Breaking changes").
- `sentinel.ErrMgmtHTTPShutdownTimeout` (see "Breaking changes").
- Experimental `WithItemAwareEviction` option / `IAlgorithmItemAware` interface / `LRUItemAware` /
  `ShardedItemAware` types — landed briefly during the RFC 0001 spike, then torn out per the RFC's own
  discipline when the perf gate failed. The [RFC document](docs/rfcs/0001-backend-owned-eviction.md) preserves
  the measurement and the lessons.

Unreleased: <https://github.com/hyp3rd/hypercache/compare/v0.5.0...HEAD> Released:
[0.5.0](https://github.com/hyp3rd/hypercache/releases/tag/v0.5.0)
