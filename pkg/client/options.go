package client

import (
	"encoding/base64"
	"log/slog"
	"net/http"
	"time"

	"github.com/hyp3rd/ewrap"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

// Option configures the Client at construction time. Options are
// applied in order; later options can override earlier ones (the
// last auth option wins, for example).
type Option func(*Client) error

// Static option errors. Surface here as wrapped %w values so callers
// can errors.Is against the specific failure mode.
var (
	errBearerEmpty    = ewrap.New("client: WithBearerAuth: empty token")
	errBasicEmpty     = ewrap.New("client: WithBasicAuth: empty username or password")
	errOIDCEmpty      = ewrap.New("client: WithOIDCClientCredentials: missing required field")
	errHTTPClientNil  = ewrap.New("client: WithHTTPClient: nil *http.Client")
	errRefreshTooFast = ewrap.New("client: WithTopologyRefresh: interval below 1s floor")
)

// minRefreshInterval is the floor below which topology refresh
// becomes self-defeating: clients hitting /cluster/members faster
// than once per second add more load than they save. Tighter intervals
// are caught at construction; the floor doesn't apply to manually-
// triggered refreshes via RefreshTopology (which exists for tests
// and operator-driven refreshes).
const minRefreshInterval = time.Second

// WithBearerAuth wires a static bearer token. Every request includes
// `Authorization: Bearer <token>`. The token does NOT get refreshed —
// for short-lived tokens (OIDC), use WithOIDCClientCredentials
// instead.
//
// Mutually exclusive with WithBasicAuth and WithOIDCClientCredentials
// — applying multiple auth options keeps the last one (the underlying
// transport is replaced wholesale).
func WithBearerAuth(token string) Option {
	return func(c *Client) error {
		if token == "" {
			return errBearerEmpty
		}

		c.http = httpClientWithAuth(c.http, bearerAuthTransport(token))

		return nil
	}
}

// WithBasicAuth wires HTTP Basic auth. Every request includes
// `Authorization: Basic base64(username:password)`. Requires the
// server to be configured with a matching `users:` block in
// HYPERCACHE_AUTH_CONFIG.
//
// The cache rejects Basic over plaintext by default — set
// `allow_basic_without_tls: true` server-side only for dev stacks.
// The client does NOT enforce this client-side (the server does);
// running this option against an http://-prefixed endpoint will
// silently leak the password if the server lets you.
func WithBasicAuth(username, password string) Option {
	return func(c *Client) error {
		if username == "" || password == "" {
			return errBasicEmpty
		}

		encoded := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))

		c.http = httpClientWithAuth(c.http, bearerLikeAuthTransport("Basic "+encoded))

		return nil
	}
}

// WithOIDCClientCredentials wires the OAuth2 client-credentials
// flow. The client fetches an access token from the IdP, caches it
// in memory, refreshes before expiry, and presents it as a bearer
// on every cache request — all transparent to the caller.
//
// Required cfg fields: ClientID, ClientSecret, TokenURL. Scopes and
// EndpointParams are optional but typically needed (many IdPs
// require an `audience` request param via EndpointParams for the
// resulting JWT's aud claim to match the cache's expectation).
//
// See the distributed-oidc-client example for the discovery flow
// that resolves TokenURL from the IdP's
// /.well-known/openid-configuration document.
func WithOIDCClientCredentials(cfg clientcredentials.Config) Option {
	return func(c *Client) error {
		if cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.TokenURL == "" {
			return errOIDCEmpty
		}

		// oauth2.NewClient returns an *http.Client whose Transport
		// auto-injects Authorization: Bearer <token> and handles
		// refresh transparently. Pass the existing Transport as
		// the base so any prior WithHTTPClient (e.g. mTLS) is
		// preserved.
		base := c.http
		if base == nil {
			base = http.DefaultClient
		}

		baseCtx := contextWithBaseHTTP(base)

		// Wrap the underlying TokenSource so rotations log via the
		// client's slog.Logger. The wrapper holds a pointer to the
		// Client so WithLogger applied AFTER this option still
		// reaches the refresh log surface — c.logger is read at
		// rotation time, not capture time.
		source := newLoggingTokenSource(cfg.TokenSource(baseCtx), c)

		c.http = oauth2.NewClient(baseCtx, source)

		return nil
	}
}

// WithHTTPClient injects a pre-configured *http.Client. Use this to
// supply a custom Transport (mTLS, custom dialer, connection-pool
// tuning) that the rest of the client builds on. Auth options
// applied after this one will wrap the Transport; auth options
// applied before may be overwritten.
//
// The injected client's Timeout is honored as the per-request
// deadline ceiling; nil Timeout means no timeout (the caller's
// context still applies).
func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) error {
		if httpClient == nil {
			return errHTTPClientNil
		}

		c.http = httpClient

		return nil
	}
}

// WithTopologyRefresh enables periodic refresh of the client's
// view of the cluster. On each tick the client GETs
// /cluster/members from any reachable endpoint and replaces its
// in-memory endpoint list with the alive-or-suspect members'
// API addresses.
//
// Pass 0 (or any negative duration) to disable refresh — the
// client will use only the seeds supplied to New for its lifetime.
//
// Intervals below 1 second are rejected at construction. The
// floor exists because /cluster/members serializes a full
// membership snapshot; clients hammering it faster than 1s add
// more load than they save.
func WithTopologyRefresh(interval time.Duration) Option {
	return func(c *Client) error {
		if interval > 0 && interval < minRefreshInterval {
			return errRefreshTooFast
		}

		c.refreshInterval = interval

		return nil
	}
}

// WithLogger sets the structured logger the client uses for
// background events (topology refresh outcomes, failover decisions).
// Defaults to a discard handler so embedded use stays silent.
// Passing nil resets to the default.
func WithLogger(logger *slog.Logger) Option {
	return func(c *Client) error {
		if logger == nil {
			c.logger = slog.New(slog.DiscardHandler)
		} else {
			c.logger = logger
		}

		return nil
	}
}
