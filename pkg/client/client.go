package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/oauth2"
)

// defaultHTTPTimeout is the per-request deadline ceiling the client
// uses when neither WithHTTPClient nor caller-side context.WithTimeout
// constrains the call. Tuned for the synchronous-RPC shape of cache
// commands: long enough to ride out a ~100ms primary failover, short
// enough that a hung node doesn't pin a goroutine indefinitely.
const defaultHTTPTimeout = 10 * time.Second

// Client speaks the hypercache-server REST API. Construct via New
// with at least one seed endpoint; use the command methods to
// dispatch operations against the cluster. Close cleanly when done
// to stop the topology-refresh loop.
//
// Client is safe for concurrent use by multiple goroutines.
type Client struct {
	// seeds is the original endpoint list supplied to New. Never
	// mutated; used as a fallback when the refreshed endpoint
	// view is empty (e.g. all known endpoints unreachable mid-
	// partition — see RFC 0003 open question 5).
	seeds []string

	// endpoints is the current working view. Updated atomically
	// by the topology refresh loop; readers (the do() dispatch
	// path) snapshot via Load.
	endpoints atomic.Pointer[[]string]

	// http is the underlying HTTP client. Auth options replace
	// its Transport; WithHTTPClient replaces the whole thing.
	http *http.Client

	// failoverRand is the source we shuffle the endpoint order
	// from. Seeded once at construction so different Clients in
	// the same process don't synchronize on each other's failover
	// decisions.
	failoverRand   *failoverShuffler
	failoverRandMu sync.Mutex

	// refreshInterval controls the topology refresh loop. 0 = disabled.
	refreshInterval time.Duration
	refreshStopCh   chan struct{}
	refreshDoneCh   chan struct{}

	logger *slog.Logger
}

// New constructs a Client. seeds must contain at least one base
// URL (e.g. "https://cache.example.com:8080"); the client uses them
// in random order and falls back to them if topology refresh ever
// wipes its endpoint view.
//
// Without any auth option the client makes anonymous requests —
// fine for dev, will 401 against any production cluster.
func New(seeds []string, opts ...Option) (*Client, error) {
	if len(seeds) == 0 {
		return nil, ErrNoEndpoints
	}

	cleaned := make([]string, 0, len(seeds))
	for _, s := range seeds {
		trimmed := strings.TrimRight(strings.TrimSpace(s), "/")
		if trimmed == "" {
			continue
		}

		cleaned = append(cleaned, trimmed)
	}

	if len(cleaned) == 0 {
		return nil, ErrNoEndpoints
	}

	c := &Client{
		seeds:        cleaned,
		http:         &http.Client{Timeout: defaultHTTPTimeout},
		failoverRand: newFailoverShuffler(),
		logger:       slog.New(slog.DiscardHandler),
	}
	c.endpoints.Store(&cleaned)

	for _, opt := range opts {
		err := opt(c)
		if err != nil {
			return nil, err
		}
	}

	if c.refreshInterval > 0 {
		c.startTopologyRefresh()
	}

	return c, nil
}

// Endpoints returns a snapshot of the current working endpoint
// list — the seeds initially, replaced by /cluster/members entries
// once topology refresh runs.
func (c *Client) Endpoints() []string {
	snap := c.endpoints.Load()
	if snap == nil {
		return append([]string(nil), c.seeds...)
	}

	return append([]string(nil), (*snap)...)
}

// Close stops the topology-refresh loop. Idempotent; subsequent
// calls are no-ops. Pending requests are NOT cancelled; callers
// should cancel via their request context if needed.
func (c *Client) Close() error {
	if c.refreshStopCh != nil {
		select {
		case <-c.refreshStopCh:
			// already closed
		default:
			close(c.refreshStopCh)
		}

		if c.refreshDoneCh != nil {
			<-c.refreshDoneCh
		}
	}

	return nil
}

// --- commands ---

// Set stores key=value with the given TTL. TTL <= 0 means no
// expiration. Returns nil on success; ErrUnauthorized / ErrForbidden
// / ErrBadRequest on auth/scope/shape problems; wrapped
// ErrAllEndpointsFailed when failover exhausted every endpoint.
func (c *Client) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	path := "/v1/cache/" + url.PathEscape(key)
	if ttl > 0 {
		path += "?ttl=" + ttl.String()
	}

	resp, err := c.do(ctx, http.MethodPut, path, bytes.NewReader(value), map[string]string{
		"Content-Type": "application/octet-stream",
	})
	if err != nil {
		return err
	}
	defer closeBody(resp)

	if resp.StatusCode != http.StatusOK {
		return classifyResponse(resp)
	}

	return nil
}

// Get returns the raw bytes stored at key. Use GetItem if you need
// metadata (version, owners, expiry) alongside the value.
func (c *Client) Get(ctx context.Context, key string) ([]byte, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/cache/"+url.PathEscape(key), nil, nil)
	if err != nil {
		return nil, err
	}
	defer closeBody(resp)

	if resp.StatusCode != http.StatusOK {
		return nil, classifyResponse(resp)
	}

	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, fmt.Errorf("read body: %w", readErr)
	}

	return body, nil
}

// GetItem returns the full Item envelope (value + metadata).
// Internally this sends Accept: application/json so the server
// returns the JSON envelope instead of raw bytes.
func (c *Client) GetItem(ctx context.Context, key string) (*Item, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/cache/"+url.PathEscape(key), nil, map[string]string{
		"Accept": contentTypeJSON,
	})
	if err != nil {
		return nil, err
	}
	defer closeBody(resp)

	if resp.StatusCode != http.StatusOK {
		return nil, classifyResponse(resp)
	}

	var env itemEnvelope

	decodeErr := json.NewDecoder(resp.Body).Decode(&env)
	if decodeErr != nil {
		return nil, fmt.Errorf("decode envelope: %w", decodeErr)
	}

	value, valueErr := decodeEnvelopeValue(env)
	if valueErr != nil {
		return nil, valueErr
	}

	return &Item{
		Key:         env.Key,
		Value:       value,
		TTLMs:       env.TTLMs,
		ExpiresAt:   env.ExpiresAt,
		Version:     env.Version,
		Origin:      env.Origin,
		LastUpdated: env.LastUpdated,
		Node:        env.Node,
		Owners:      env.Owners,
	}, nil
}

// Delete removes the key from the cluster. Returns nil on success
// (including the case where the key didn't exist — DELETE is
// idempotent in REST terms).
func (c *Client) Delete(ctx context.Context, key string) error {
	resp, err := c.do(ctx, http.MethodDelete, "/v1/cache/"+url.PathEscape(key), nil, nil)
	if err != nil {
		return err
	}
	defer closeBody(resp)

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return classifyResponse(resp)
	}

	return nil
}

// Identity returns the caller's resolved identity — the response
// from GET /v1/me. Use at startup as a "is my token valid against
// this cluster?" canary, or to introspect capabilities before
// attempting scope-protected operations.
func (c *Client) Identity(ctx context.Context) (*Identity, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/me", nil, nil)
	if err != nil {
		return nil, err
	}
	defer closeBody(resp)

	if resp.StatusCode != http.StatusOK {
		return nil, classifyResponse(resp)
	}

	var out meResponse

	decodeErr := json.NewDecoder(resp.Body).Decode(&out)
	if decodeErr != nil {
		return nil, fmt.Errorf("decode /v1/me: %w", decodeErr)
	}

	return &Identity{
		ID:           out.ID,
		Scopes:       out.Scopes,
		Capabilities: out.Capabilities,
	}, nil
}

// Can probes whether the caller's resolved identity holds the
// given capability. Cheaper than the speculative-write pattern
// (try the action, catch 403) and stable across future
// scope-to-capability refactors — clients key off the capability
// string, not the internal scope shape.
//
// Returns (true, nil) when the cluster confirms the capability,
// (false, nil) when it confirms the capability is missing, and
// (false, err) when the probe itself failed (network, auth,
// unknown capability — `errors.Is(err, ErrBadRequest)` discriminates
// the spelling-mistake case).
//
// Pair with the canonical at-startup canary:
//
//	can, err := c.Can(ctx, "cache.write")
//	if err != nil { log.Fatal(err) }
//	if !can { log.Fatal("this credential cannot write") }
func (c *Client) Can(ctx context.Context, capability string) (bool, error) {
	path := "/v1/me/can?capability=" + url.QueryEscape(capability)

	resp, err := c.do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return false, err
	}
	defer closeBody(resp)

	if resp.StatusCode != http.StatusOK {
		return false, classifyResponse(resp)
	}

	var out struct {
		Capability string `json:"capability"`
		Allowed    bool   `json:"allowed"`
	}

	decodeErr := json.NewDecoder(resp.Body).Decode(&out)
	if decodeErr != nil {
		return false, fmt.Errorf("decode /v1/me/can: %w", decodeErr)
	}

	return out.Allowed, nil
}

// --- internal helpers used by options.go ---

// bearerAuthTransport returns a RoundTripper that injects the
// given bearer token on every request.
func bearerAuthTransport(token string) http.RoundTripper {
	return bearerLikeAuthTransport("Bearer " + token)
}

// bearerLikeAuthTransport returns a RoundTripper that injects the
// given Authorization header value on every request. Used by both
// bearer and Basic auth — both share the "set one header on every
// request" shape.
func bearerLikeAuthTransport(headerValue string) http.RoundTripper {
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		// Clone before mutating so the caller's copy stays clean.
		cloned := req.Clone(req.Context())
		cloned.Header.Set("Authorization", headerValue)

		base := http.DefaultTransport

		resp, err := base.RoundTrip(cloned)
		if err != nil {
			return nil, fmt.Errorf("client transport: %w", err)
		}

		return resp, nil
	})
}

// httpClientWithAuth wraps the existing http.Client with an
// authentication transport. The original client's Timeout is
// preserved.
func httpClientWithAuth(base *http.Client, authTransport http.RoundTripper) *http.Client {
	out := &http.Client{Transport: authTransport}
	if base != nil {
		out.Timeout = base.Timeout
	}

	return out
}

// contextWithBaseHTTP returns a context carrying base as the
// oauth2.HTTPClient value. Used so oauth2.NewClient's TokenSource
// uses our base transport (custom timeout, custom mTLS Transport)
// rather than http.DefaultClient when talking to the IdP.
func contextWithBaseHTTP(base *http.Client) context.Context {
	if base == nil {
		return context.Background()
	}

	return context.WithValue(context.Background(), oauth2.HTTPClient, base)
}

// roundTripperFunc is the function-as-RoundTripper adapter the
// stdlib should ship but doesn't.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// decodeEnvelopeValue returns the raw bytes from an itemEnvelope.
// The server always emits base64 in the envelope, but we tolerate
// the (currently-unused) raw encoding for forward-compatibility.
func decodeEnvelopeValue(env itemEnvelope) ([]byte, error) {
	switch env.ValueEncoding {
	case "", "base64":
		decoded, err := base64.StdEncoding.DecodeString(env.Value)
		if err != nil {
			return nil, fmt.Errorf("decode base64 value: %w", err)
		}

		return decoded, nil

	default:
		return []byte(env.Value), nil
	}
}

// closeBody discards the close error. The body is fully drained on
// a successful response, and a close failure is not actionable —
// the connection is already being returned to the pool or torn
// down by the runtime.
func closeBody(resp *http.Response) {
	_ = resp.Body.Close()
}
