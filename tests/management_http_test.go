package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/longbridgeapp/assert"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/pkg/backend"
)

// TestManagementHTTP_BasicEndpoints spins up the management HTTP server on an ephemeral port
// and validates core endpoints.
func TestManagementHTTP_BasicEndpoints(t *testing.T) {
	cfg := hypercache.NewConfig[backend.InMemory](constants.InMemoryBackend)
	cfg.HyperCacheOptions = append(cfg.HyperCacheOptions,
		hypercache.WithEvictionInterval[backend.InMemory](0),
		hypercache.WithManagementHTTP[backend.InMemory]("127.0.0.1:0"),
	)

	ctx := context.Background()
	hc, err := hypercache.New(ctx, hypercache.GetDefaultManager(), cfg)
	assert.Nil(t, err)
	defer hc.Stop(ctx)

	// wait briefly for listener
	time.Sleep(30 * time.Millisecond)

	addr := hc.ManagementHTTPAddress()
	assert.True(t, addr != "")

	client := &http.Client{Timeout: 2 * time.Second}

	// /health
	resp, err := client.Get("http://" + addr + "/health")
	assert.Nil(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()

	// /stats
	resp, err = client.Get("http://" + addr + "/stats")
	assert.Nil(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var statsBody map[string]any

	dec := json.NewDecoder(resp.Body)
	err = dec.Decode(&statsBody)
	assert.NoError(t, err)
	assert.True(t, resp.StatusCode == http.StatusOK)
	_ = resp.Body.Close()

	// /config
	resp, err = client.Get("http://" + addr + "/config")
	assert.Nil(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var cfgBody map[string]any

	dec = json.NewDecoder(resp.Body)
	_ = dec.Decode(&cfgBody)
	_ = resp.Body.Close()

	assert.True(t, cfgBody["evictionAlgorithm"] != nil)
}
