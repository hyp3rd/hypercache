package tests

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/longbridgeapp/assert"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/pkg/backend"
)

// TestManagementHTTP_BasicEndpoints spins up the management HTTP server on an ephemeral port
// and validates core endpoints.
func TestManagementHTTP_BasicEndpoints(t *testing.T) {
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
	assert.Nil(t, err)

	defer hc.Stop(ctx)

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

	// /health
	resp, err := client.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	_ = resp.Body.Close()

	// /stats
	resp, err = client.Get("http://" + addr + "/stats")
	if err != nil {
		t.Fatalf("GET /stats: %v", err)
	}

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var statsBody map[string]any

	dec := json.NewDecoder(resp.Body)

	err = dec.Decode(&statsBody)
	assert.NoError(t, err)

	_ = resp.Body.Close()

	// /config
	resp, err = client.Get("http://" + addr + "/config")
	if err != nil {
		t.Fatalf("GET /config: %v", err)
	}

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var cfgBody map[string]any

	dec = json.NewDecoder(resp.Body)
	_ = dec.Decode(&cfgBody)
	_ = resp.Body.Close()

	assert.True(t, len(cfgBody) > 0)

	assert.True(t, cfgBody["evictionAlgorithm"] != nil)
}
