// Example: connecting to a hypercache-server cluster using the
// `pkg/client` SDK with OIDC client-credentials authentication.
//
// This is the recommended shape for Go consumers. The SDK handles
// auth-header injection, token refresh, endpoint failover, topology
// refresh, content negotiation, and typed errors — everything the
// hand-rolled raw example in distributed-oidc-client-raw/ has to do
// by hand against net/http.
//
// See ../../docs/client-sdk.md for the full SDK reference.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hyp3rd/ewrap"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/hyp3rd/hypercache/pkg/client"
)

const (
	exampleKey   = "example-key"
	exampleValue = "hello from sdk"
	exampleTTL   = 5 * time.Minute

	topologyRefresh = 30 * time.Second
	discoveryPath   = "/.well-known/openid-configuration"
)

// errEnvMissing is the sentinel mustEnv wraps when a required
// variable is absent. Kept static so failure-mode tests could
// errors.Is against it; in the example, run() surfaces the
// wrapped error to stderr.
var (
	errEnvMissing          = ewrap.New("missing required env var")
	errDiscoveryNoEndpoint = ewrap.New("OIDC discovery doc missing token_endpoint")
)

func main() {
	err := run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// run is the testable main body. Splitting it from main() so
// `defer c.Close()` actually executes on every error path —
// a defer next to a log.Fatal in main() would silently skip.
func run() error {
	issuer, err := mustEnv("OIDC_ISSUER")
	if err != nil {
		return err
	}

	clientID, err := mustEnv("OIDC_CLIENT_ID")
	if err != nil {
		return err
	}

	clientSecret, err := mustEnv("OIDC_CLIENT_SECRET")
	if err != nil {
		return err
	}

	audience, err := mustEnv("OIDC_AUDIENCE")
	if err != nil {
		return err
	}

	endpoints := strings.Fields(envOr("HYPERCACHE_ENDPOINTS", "http://localhost:8080"))

	tokenURL, err := discoverTokenEndpoint(issuer)
	if err != nil {
		return fmt.Errorf("OIDC discovery: %w", err)
	}

	c, err := client.New(
		endpoints,
		client.WithOIDCClientCredentials(clientcredentials.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			TokenURL:     tokenURL,
			Scopes:       strings.Fields(envOr("OIDC_SCOPES", "openid")),
			EndpointParams: url.Values{
				"audience": {audience},
			},
		}),
		client.WithTopologyRefresh(topologyRefresh),
	)
	if err != nil {
		return fmt.Errorf("client.New: %w", err)
	}

	defer func() { _ = c.Close() }()

	return demo(context.Background(), c)
}

// demo runs the canonical introspect → set → get → delete sequence
// against the SDK. Each step prints its result so the operator can
// see exactly what worked.
func demo(ctx context.Context, c *client.Client) error {
	id, err := c.Identity(ctx)
	if err != nil {
		return fmt.Errorf("identity: %w", err)
	}

	fmt.Fprintf(os.Stdout, "authed as %s with %v\n", id.ID, id.Capabilities)

	err = c.Set(ctx, exampleKey, []byte(exampleValue), exampleTTL)
	if err != nil {
		return fmt.Errorf("set: %w", err)
	}

	val, err := c.Get(ctx, exampleKey)
	if err != nil {
		return fmt.Errorf("get: %w", err)
	}

	fmt.Fprintf(os.Stdout, "Get(%q) = %q\n", exampleKey, val)

	err = c.Delete(ctx, exampleKey)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}

	fmt.Fprintln(os.Stdout, "deleted")

	// Batch shape: write three keys in a single round-trip. The
	// per-item Stored / Err fields surface per-key results; the
	// outer error fires only on transport / auth / HTTP-level
	// failures (4xx / 5xx).
	batchResults, err := c.BatchSet(ctx, []client.BatchSetItem{
		{Key: "batch-1", Value: []byte("one"), TTL: time.Minute},
		{Key: "batch-2", Value: []byte("two"), TTL: time.Minute},
		{Key: "batch-3", Value: []byte("three"), TTL: time.Minute},
	})
	if err != nil {
		return fmt.Errorf("batch set: %w", err)
	}

	for _, r := range batchResults {
		if r.Stored {
			fmt.Fprintf(os.Stdout, "batch stored %s (%d bytes)\n", r.Key, r.Bytes)
		} else {
			fmt.Fprintf(os.Stdout, "batch %s FAILED: %v\n", r.Key, r.Err)
		}
	}

	return nil
}

// --- env helpers ---

func envOr(name, fallback string) string {
	v := os.Getenv(name)
	if v == "" {
		return fallback
	}

	return v
}

func mustEnv(name string) (string, error) {
	v := os.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("%w: %s", errEnvMissing, name)
	}

	return v, nil
}

// discoverTokenEndpoint fetches the IdP's `.well-known/openid-configuration`
// and returns its `token_endpoint`. The SDK doesn't take on OIDC discovery
// itself — that's an IdP concern, not a cache concern — so consumer code
// (here, this example) wires the discovery dance and hands the resolved
// URL to clientcredentials.Config.
func discoverTokenEndpoint(issuer string) (string, error) {
	issuer = strings.TrimRight(issuer, "/")

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, issuer+discoveryPath, nil)
	if err != nil {
		return "", fmt.Errorf("build discovery request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("discovery request: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("discovery: %w: %s", errDiscoveryNoEndpoint, resp.Status)
	}

	var doc struct {
		TokenEndpoint string `json:"token_endpoint"`
	}

	err = json.NewDecoder(resp.Body).Decode(&doc)
	if err != nil {
		return "", fmt.Errorf("decode discovery: %w", err)
	}

	if doc.TokenEndpoint == "" {
		return "", errDiscoveryNoEndpoint
	}

	return doc.TokenEndpoint, nil
}
