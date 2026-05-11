// Example: connecting to a hypercache-server cluster as a backend
// service, authenticating via OIDC client_credentials, and exercising
// the full client API surface.
//
// This is a runnable demo, not production-grade code. The goal is to
// show:
//
//  1. How a service obtains an OIDC access token (no human-in-the-loop)
//     using the standard golang.org/x/oauth2/clientcredentials flow.
//  2. How the token is automatically attached to every cache request
//     via oauth2.NewClient's transport wrapper — there's no manual
//     header bookkeeping.
//  3. How GET /v1/me lets the client introspect the bound identity
//     before doing real work (canary: "is my token actually valid
//     against this cluster?").
//  4. The PUT / GET / DELETE / batch surface against /v1/cache/*.
//
// See README.md in this directory for setup instructions (env vars,
// IdP setup, scope mapping).
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/goccy/go-json"
	"github.com/hyp3rd/ewrap"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

const (
	defaultEndpoint = "http://localhost:8080"
	defaultScopes   = "openid"
	httpTimeout     = 10 * time.Second
	putTTL          = 5 * time.Minute
	batchTTL        = time.Minute
)

// Static sentinel errors — err113 forbids defining dynamic errors at
// call sites. Wrapping these with %w lets downstream callers
// errors.Is against each failure mode.
var (
	errIssuerMissing    = ewrap.New("OIDC_ISSUER is required")
	errAudienceMissing  = ewrap.New("OIDC_AUDIENCE is required (must match the cache's HYPERCACHE_OIDC_AUDIENCE)")
	errClientIDMissing  = ewrap.New("OIDC_CLIENT_ID is required")
	errSecretMissing    = ewrap.New("OIDC_CLIENT_SECRET is required")
	errDiscoveryHTTP    = ewrap.New("OIDC discovery returned non-200")
	errDiscoveryMissing = ewrap.New("OIDC discovery doc missing token_endpoint")
	errCacheStatus      = ewrap.New("cache returned non-2xx")
)

// envConfig is the full set of knobs the demo reads from the
// environment. Mirroring HYPERCACHE_OIDC_* names where they overlap
// with the server keeps operator mental-models consistent.
type envConfig struct {
	cacheEndpoint string // e.g. https://cache.example.com:8080
	oidcIssuer    string // e.g. https://keycloak.example.com/realms/cache
	oidcAudience  string // must match the server's HYPERCACHE_OIDC_AUDIENCE
	oidcClientID  string
	oidcSecret    string
	oidcScopes    []string // OAuth scopes requested; e.g. "openid", "cache:read", "cache:write"
}

func loadEnv() (envConfig, error) {
	cfg := envConfig{
		cacheEndpoint: envOr("HYPERCACHE_ENDPOINT", defaultEndpoint),
		oidcIssuer:    os.Getenv("OIDC_ISSUER"),
		oidcAudience:  os.Getenv("OIDC_AUDIENCE"),
		oidcClientID:  os.Getenv("OIDC_CLIENT_ID"),
		oidcSecret:    os.Getenv("OIDC_CLIENT_SECRET"),
		oidcScopes:    parseScopes(envOr("OIDC_SCOPES", defaultScopes)),
	}

	switch {
	case cfg.oidcIssuer == "":
		return envConfig{}, errIssuerMissing
	case cfg.oidcAudience == "":
		return envConfig{}, errAudienceMissing
	case cfg.oidcClientID == "":
		return envConfig{}, errClientIDMissing
	case cfg.oidcSecret == "":
		return envConfig{}, errSecretMissing
	}

	return cfg, nil
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}

	return fallback
}

func parseScopes(raw string) []string {
	parts := strings.Fields(raw)
	if len(parts) == 0 {
		return []string{"openid"}
	}

	return parts
}

func main() {
	err := run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadEnv()
	if err != nil {
		return err
	}

	client, err := buildClient(context.Background(), cfg)
	if err != nil {
		return err
	}

	return demoFlow(context.Background(), client)
}

// buildClient performs the one-time OIDC setup: discover the token
// endpoint, configure the clientcredentials source, wrap an
// http.Client whose transport auto-injects the bearer header.
func buildClient(ctx context.Context, cfg envConfig) (*cacheClient, error) {
	tokenURL, err := discoverTokenEndpoint(ctx, cfg.oidcIssuer)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery: %w", err)
	}

	// clientcredentials handles the dance: caches tokens in memory,
	// refreshes them before expiry, surfaces transport errors without
	// retrying blindly. The audience request parameter is set via
	// EndpointParams — most IdPs (Auth0, Okta, Keycloak with the
	// audience mapper) require it for the resulting JWT's aud claim
	// to match the cache's expectation.
	ccCfg := &clientcredentials.Config{
		ClientID:     cfg.oidcClientID,
		ClientSecret: cfg.oidcSecret,
		TokenURL:     tokenURL,
		Scopes:       cfg.oidcScopes,
		EndpointParams: url.Values{
			"audience": {cfg.oidcAudience},
		},
		AuthStyle: oauth2.AuthStyleInParams,
	}

	// oauth2.NewClient returns an *http.Client whose Transport
	// auto-injects Authorization: Bearer <token> and refreshes the
	// token transparently. This is the single most important
	// affordance of x/oauth2 — every cache call below is a plain
	// http.Client.Do with no header bookkeeping.
	httpClient := oauth2.NewClient(ctx, ccCfg.TokenSource(ctx))

	httpClient.Timeout = httpTimeout

	return &cacheClient{
		endpoint: strings.TrimRight(cfg.cacheEndpoint, "/"),
		http:     httpClient,
	}, nil
}

// demoFlow runs the canonical client API sequence: introspect, write,
// read raw, read envelope, delete, batch write. Each step prints its
// outcome so the operator can see exactly what worked and what didn't.
func demoFlow(ctx context.Context, client *cacheClient) error {
	out := os.Stdout

	fmt.Fprintln(out, "== /v1/me (verify bound identity) ==")

	me, err := client.me(ctx)
	if err != nil {
		return fmt.Errorf("me: %w", err)
	}

	fmt.Fprintf(out, "  resolved identity: %s\n", me.ID)
	fmt.Fprintf(out, "  granted scopes:    %v\n\n", me.Scopes)

	fmt.Fprintln(out, "== PUT /v1/cache/example-key (5 min TTL) ==")

	err = client.put(ctx, "example-key", []byte("hello from oidc client"), putTTL)
	if err != nil {
		return fmt.Errorf("put: %w", err)
	}

	fmt.Fprintln(out, "  stored")
	fmt.Fprintln(out)

	fmt.Fprintln(out, "== GET /v1/cache/example-key (raw bytes) ==")

	value, err := client.getRaw(ctx, "example-key")
	if err != nil {
		return fmt.Errorf("get raw: %w", err)
	}

	fmt.Fprintf(out, "  value: %q\n\n", value)

	fmt.Fprintln(out, "== GET /v1/cache/example-key (JSON envelope) ==")

	env, err := client.getEnvelope(ctx, "example-key")
	if err != nil {
		return fmt.Errorf("get envelope: %w", err)
	}

	fmt.Fprintf(out, "  key:      %s\n", env.Key)
	fmt.Fprintf(out, "  version:  %d\n", env.Version)
	fmt.Fprintf(out, "  owners:   %v\n", env.Owners)
	fmt.Fprintf(out, "  encoding: %s\n\n", env.ValueEncoding)

	fmt.Fprintln(out, "== DELETE /v1/cache/example-key ==")

	err = client.delete(ctx, "example-key")
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}

	fmt.Fprintln(out, "  deleted")
	fmt.Fprintln(out)

	fmt.Fprintln(out, "== batch PUT /v1/cache/batch/put (3 keys) ==")

	err = client.batchPut(ctx, map[string][]byte{
		"batch-1": []byte("one"),
		"batch-2": []byte("two"),
		"batch-3": []byte("three"),
	}, batchTTL)
	if err != nil {
		return fmt.Errorf("batch put: %w", err)
	}

	fmt.Fprintln(out, "  stored 3 keys")

	return nil
}

// --- cache client ---

// cacheClient is the thin wrapper around the typed REST surface.
// Production users would lift this into a package; the example keeps
// it inline so all the wire shapes are visible in one file.
type cacheClient struct {
	endpoint string
	http     *http.Client
}

// meResponse mirrors the server's wire type. Duplicated rather than
// shared because clients should depend on the JSON contract, not the
// server's internal struct.
type meResponse struct {
	ID     string   `json:"id"`
	Scopes []string `json:"scopes"`
}

type itemEnvelope struct {
	Key           string   `json:"key"`
	Value         string   `json:"value"`
	ValueEncoding string   `json:"value_encoding"`
	TTLMs         int64    `json:"ttl_ms,omitempty"`
	ExpiresAt     string   `json:"expires_at,omitempty"`
	Version       uint64   `json:"version"`
	Origin        string   `json:"origin,omitempty"`
	LastUpdated   string   `json:"last_updated,omitempty"`
	Node          string   `json:"node"`
	Owners        []string `json:"owners"`
}

// errorResponse is the canonical 4xx/5xx envelope the cache returns.
// Surfacing the code field gives callers stable error-classification
// without sniffing HTTP status alone.
type errorResponse struct {
	Code    string `json:"code"`
	Error   string `json:"error"`
	Details string `json:"details,omitempty"`
}

func (c *cacheClient) me(ctx context.Context) (*meResponse, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/me", nil, nil)
	if err != nil {
		return nil, err
	}
	defer closeBody(resp)

	if resp.StatusCode != http.StatusOK {
		return nil, classifyResponse(resp)
	}

	var out meResponse

	err = json.NewDecoder(resp.Body).Decode(&out)
	if err != nil {
		return nil, fmt.Errorf("decode /v1/me: %w", err)
	}

	return &out, nil
}

func (c *cacheClient) put(ctx context.Context, key string, value []byte, ttl time.Duration) error {
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

// getRaw uses the default content negotiation — no Accept header means
// the server returns the literal value bytes. Right for callers who
// stored bytes and want bytes back.
func (c *cacheClient) getRaw(ctx context.Context, key string) ([]byte, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/cache/"+url.PathEscape(key), nil, nil)
	if err != nil {
		return nil, err
	}
	defer closeBody(resp)

	if resp.StatusCode != http.StatusOK {
		return nil, classifyResponse(resp)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	return body, nil
}

// getEnvelope explicitly asks for the JSON envelope by setting
// Accept: application/json. Right for callers that need metadata
// (version, owners, expiry) alongside the value.
func (c *cacheClient) getEnvelope(ctx context.Context, key string) (*itemEnvelope, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/cache/"+url.PathEscape(key), nil, map[string]string{
		"Accept": "application/json",
	})
	if err != nil {
		return nil, err
	}
	defer closeBody(resp)

	if resp.StatusCode != http.StatusOK {
		return nil, classifyResponse(resp)
	}

	var env itemEnvelope

	err = json.NewDecoder(resp.Body).Decode(&env)
	if err != nil {
		return nil, fmt.Errorf("decode envelope: %w", err)
	}

	return &env, nil
}

func (c *cacheClient) delete(ctx context.Context, key string) error {
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

// batchPutRequest mirrors the server's POST /v1/cache/batch/put body
// shape. Values are base64-encoded for binary-safety on the JSON wire.
type batchPutRequest struct {
	Items []batchPutItem `json:"items"`
}

type batchPutItem struct {
	Key   string `json:"key"`
	Value string `json:"value"` // base64-encoded
	TTLMs int64  `json:"ttl_ms,omitempty"`
}

func (c *cacheClient) batchPut(ctx context.Context, items map[string][]byte, ttl time.Duration) error {
	body := batchPutRequest{Items: make([]batchPutItem, 0, len(items))}
	for k, v := range items {
		body.Items = append(body.Items, batchPutItem{
			Key:   k,
			Value: base64.StdEncoding.EncodeToString(v),
			TTLMs: ttl.Milliseconds(),
		})
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal batch put: %w", err)
	}

	resp, err := c.do(ctx, http.MethodPost, "/v1/cache/batch/put", bytes.NewReader(encoded), map[string]string{
		"Content-Type": "application/json",
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

// do constructs an HTTP request with the given headers and dispatches
// it via the oauth2-wrapped client. Centralizing request construction
// keeps each verb method short and ensures every request runs through
// the bearer-injecting transport.
func (c *cacheClient) do(
	ctx context.Context,
	method, path string,
	body io.Reader,
	headers map[string]string,
) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, body)
	if err != nil {
		return nil, fmt.Errorf("build %s %s: %w", method, path, err)
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dispatch %s %s: %w", method, path, err)
	}

	return resp, nil
}

// closeBody discards Close errors. The body is fully drained on a
// successful response and we don't have anything actionable to do if
// the underlying connection's close fails (the connection is already
// being returned to the pool / torn down).
func closeBody(resp *http.Response) {
	_ = resp.Body.Close()
}

// classifyResponse parses the cache's canonical error envelope and
// returns a typed error. Falls back to the raw status when the body
// doesn't parse — that should only happen if a load balancer returns
// its own non-JSON 5xx ahead of the cache.
func classifyResponse(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)

	var envelope errorResponse

	err := json.Unmarshal(body, &envelope)
	if err != nil || envelope.Code == "" {
		return fmt.Errorf("%w: %s: %s", errCacheStatus, resp.Status, strings.TrimSpace(string(body)))
	}

	return fmt.Errorf("%w: %s [%s]: %s", errCacheStatus, resp.Status, envelope.Code, envelope.Error)
}

// --- OIDC discovery ---

type oidcDiscovery struct {
	TokenEndpoint string `json:"token_endpoint"`
}

// discoverTokenEndpoint fetches the IdP's OIDC discovery document and
// returns the token_endpoint URL. Spec'd at
// https://openid.net/specs/openid-connect-discovery-1_0.html — every
// compliant IdP serves this at /.well-known/openid-configuration.
func discoverTokenEndpoint(ctx context.Context, issuer string) (string, error) {
	issuer = strings.TrimRight(issuer, "/")

	discoveryURL := issuer + "/.well-known/openid-configuration"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return "", fmt.Errorf("build discovery request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("discovery request: %w", err)
	}
	defer closeBody(resp)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: %s", errDiscoveryHTTP, resp.Status)
	}

	var doc oidcDiscovery

	err = json.NewDecoder(resp.Body).Decode(&doc)
	if err != nil {
		return "", fmt.Errorf("decode discovery: %w", err)
	}

	if doc.TokenEndpoint == "" {
		return "", errDiscoveryMissing
	}

	return doc.TokenEndpoint, nil
}
