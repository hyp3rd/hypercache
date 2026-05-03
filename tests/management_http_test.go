package tests

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/require"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/pkg/backend"
)

// TestManagementHTTP_BasicEndpoints spins up the management HTTP server on an ephemeral port
// and validates core endpoints.
func TestManagementHTTP_BasicEndpoints(t *testing.T) {
	t.Parallel()

	cfg, err := hypercache.NewConfig[backend.InMemory](constants.InMemoryBackend)
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}

	cfg.HyperCacheOptions = append(cfg.HyperCacheOptions,
		hypercache.WithEvictionInterval[backend.InMemory](0),
		hypercache.WithManagementHTTP[backend.InMemory]("127.0.0.1:0"),
	)

	ctx := context.Background()
	hc, err := hypercache.New(ctx, hypercache.GetDefaultManager(), cfg)
	require.NoError(t, err)

	t.Cleanup(func() { _ = hc.Stop(ctx) })

	// Wait for the management HTTP listener to come up. The race detector
	// can push listener startup well past the original 30 ms; poll with a
	// generous deadline instead.
	var addr string

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		addr = hc.ManagementHTTPAddress()
		if addr != "" {
			break
		}

		time.Sleep(10 * time.Millisecond)
	}

	if addr == "" {
		t.Fatal("management HTTP listener did not bind within deadline")
	}

	client := &http.Client{Timeout: 5 * time.Second}

	get := func(path string) *http.Response {
		t.Helper()

		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+path, nil)
		require.NoError(t, reqErr)

		resp, doErr := client.Do(req)
		require.NoError(t, doErr)

		return resp
	}

	// /health
	resp := get("/health")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	_ = resp.Body.Close()

	// /stats
	resp = get("/stats")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var statsBody map[string]any

	dec := json.NewDecoder(resp.Body)

	err = dec.Decode(&statsBody)
	require.NoError(t, err)

	_ = resp.Body.Close()

	// /config
	resp = get("/config")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var cfgBody map[string]any

	dec = json.NewDecoder(resp.Body)
	_ = dec.Decode(&cfgBody)
	_ = resp.Body.Close()

	require.NotEmpty(t, cfgBody)
	require.NotNil(t, cfgBody["evictionAlgorithm"])
}
