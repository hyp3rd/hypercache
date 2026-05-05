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

// waitForManagementAddr polls hc.ManagementHTTPAddress until the listener
// has bound. Under -race the listener startup can take seconds, so a
// one-shot read would race.
func waitForManagementAddr(hc *hypercache.HyperCache[backend.InMemory], timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if addr := hc.ManagementHTTPAddress(); addr != "" {
			return addr
		}

		time.Sleep(10 * time.Millisecond)
	}

	return ""
}

// TestManagementHTTP_BasicEndpoints spins up the management HTTP server on an ephemeral port
// and validates core endpoints.
func TestManagementHTTP_BasicEndpoints(t *testing.T) {
	t.Parallel()

	cfg, err := hypercache.NewConfig[backend.InMemory](constants.InMemoryBackend)
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}

	cfg.HyperCacheOptions = append(
		cfg.HyperCacheOptions,
		hypercache.WithEvictionInterval[backend.InMemory](0),
		hypercache.WithManagementHTTP[backend.InMemory]("127.0.0.1:0"),
	)

	ctx := context.Background()
	hc, err := hypercache.New(ctx, hypercache.GetDefaultManager(), cfg)
	require.NoError(t, err)

	t.Cleanup(func() { _ = hc.Stop(ctx) })

	addr := waitForManagementAddr(hc, 5*time.Second)
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

	resp := get("/health")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	_ = resp.Body.Close()

	resp = get("/stats")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var statsBody map[string]any

	require.NoError(t, json.NewDecoder(resp.Body).Decode(&statsBody))

	_ = resp.Body.Close()

	resp = get("/config")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var cfgBody map[string]any

	_ = json.NewDecoder(resp.Body).Decode(&cfgBody)
	_ = resp.Body.Close()

	require.NotEmpty(t, cfgBody)
	require.NotNil(t, cfgBody["evictionAlgorithm"])
}
