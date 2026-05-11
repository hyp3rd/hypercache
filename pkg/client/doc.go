// Package client provides a high-level Go client for hypercache-server
// clusters. It wraps the REST API with a typed surface, handles
// authentication (bearer, Basic, OIDC client-credentials), tolerates
// node failures via multi-endpoint failover, and optionally tracks
// cluster membership so new nodes become reachable without redeploying
// consumers.
//
// # Quickstart
//
// Construct a client with one or more seed endpoints and an auth
// option, then dispatch commands:
//
//	c, err := client.New(
//	    []string{"https://cache-0.example.com:8080", "https://cache-1.example.com:8080"},
//	    client.WithBearerAuth(os.Getenv("HYPERCACHE_TOKEN")),
//	    client.WithTopologyRefresh(30 * time.Second),
//	)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer c.Close()
//
//	err = c.Set(ctx, "session:user-42", payload, 5*time.Minute)
//	value, err := c.Get(ctx, "session:user-42")
//
// # Authentication
//
// Four auth modes coexist on the server. The SDK exposes three of
// them as Option helpers (mTLS users supply a pre-configured
// *http.Client via WithHTTPClient):
//
//   - WithBearerAuth(token) — static token, e.g. from
//     HYPERCACHE_AUTH_CONFIG's tokens: block.
//   - WithBasicAuth(user, password) — HTTP Basic auth against the
//     server's users: block.
//   - WithOIDCClientCredentials(cfg) — full OAuth2 client-credentials
//     flow with auto-refresh.
//   - WithHTTPClient(c) — supply your own *http.Client (mTLS,
//     custom transport, etc.).
//
// Applying multiple auth options keeps the LAST one; the underlying
// transport is replaced wholesale on each.
//
// # Failover and topology
//
// The client takes a slice of seed endpoints at construction. Each
// command picks an endpoint at random; on transport error, 5xx, or
// 503 draining it walks to the next. On 4xx (auth, scope,
// not-found, bad-request) it returns immediately — those answers
// are deterministic.
//
// WithTopologyRefresh enables a background loop that pulls
// /cluster/members on the configured interval and replaces the
// working endpoint list with the cluster's live view. New nodes
// become reachable without a client redeploy; the original seeds
// remain as a fallback if the working view ever empties (e.g.
// during a partition).
//
// # Errors
//
// Every command method returns an error that satisfies errors.Is
// against the package's sentinel set:
//
//   - client.ErrNotFound (key missing or /v1/me on a misrouted request)
//   - client.ErrUnauthorized (auth rejected)
//   - client.ErrForbidden (auth resolved but missing scope)
//   - client.ErrDraining (every endpoint reported 503)
//   - client.ErrBadRequest (malformed request shape)
//   - client.ErrInternal (cluster-side 5xx)
//   - client.ErrAllEndpointsFailed (failover exhausted)
//
// Use errors.As against *client.StatusError to extract the
// canonical Code string and Details. Sentinels are the recommended
// path for control flow; StatusError is for surfacing details to
// callers or logs.
//
// # See also
//
// Package backend hosts the cache nodes the client talks to. The
// wire protocol is the OpenAPI spec at /v1/openapi.yaml on every
// node; the client implements it but is not the contract.
package client
