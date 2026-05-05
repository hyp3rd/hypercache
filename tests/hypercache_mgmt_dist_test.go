package tests

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/goccy/go-json"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/pkg/backend"
)

// assertEndpointHasField fetches baseURL+path as JSON and asserts the
// response contains `field`. If the response carries an error label
// (constants.ErrorLabel), the test fatals with its value — this surfaces
// 404/500 from a misconfigured backend instead of the misleading "missing
// field" assertion.
func assertEndpointHasField(t *testing.T, url, field string) {
	t.Helper()

	body := getJSON(t, url)
	if _, ok := body[field]; ok {
		return
	}

	if e, hasErr := body[constants.ErrorLabel]; hasErr {
		t.Fatalf("%s returned error: %v", url, e)
	}

	t.Errorf("%s missing %s field", url, field)
}

// TestManagementHTTPDistMemory validates management endpoints for the experimental DistMemory backend.
func TestManagementHTTPDistMemory(t *testing.T) { //nolint:paralleltest // mgmt server bound to fixed port
	cfg, err := hypercache.NewConfig[backend.DistMemory](constants.DistMemoryBackend)
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}

	cfg.HyperCacheOptions = append(
		cfg.HyperCacheOptions,
		hypercache.WithManagementHTTP[backend.DistMemory]("127.0.0.1:0"),
	)
	cfg.DistMemoryOptions = []backend.DistMemoryOption{
		backend.WithDistReplication(1),
		backend.WithDistVirtualNodes(32),
		backend.WithDistNode("test-node", "local"),
	}

	hc, err := hypercache.New(context.Background(), hypercache.GetDefaultManager(), cfg)
	if err != nil {
		t.Fatalf("new dist hypercache: %v", err)
	}

	defer func() { _ = hc.Stop(context.Background()) }()

	baseURL := waitForMgmt(t, hc)

	err = hc.Set(context.Background(), "alpha", "value", 0)
	if err != nil {
		t.Fatalf("set alpha: %v", err)
	}

	configBody := getJSON(t, baseURL+"/config")
	if _, ok := configBody["replication"]; !ok {
		t.Errorf("/config missing replication")
	}

	if vnp, ok := configBody["virtualNodesPerNode"]; !ok || vnp == nil {
		t.Errorf("/config missing virtualNodesPerNode")
	}

	assertEndpointHasField(t, baseURL+"/dist/metrics", "ForwardGet")
	assertEndpointHasField(t, baseURL+"/dist/owners?key=alpha", "owners")
	assertEndpointHasField(t, baseURL+"/cluster/members", "members")
	assertEndpointHasField(t, baseURL+"/cluster/ring", "vnodes")
}

// waitForMgmt waits until management HTTP server is bound and responsive.
func waitForMgmt(t *testing.T, hc *hypercache.HyperCache[backend.DistMemory]) string {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)

	var addr string

	for time.Now().Before(deadline) {
		addr = hc.ManagementHTTPAddress()
		if addr != "" {
			resp, err := http.Get("http://" + addr + "/health") //nolint:noctx
			if err == nil && resp.StatusCode == http.StatusOK {
				_ = resp.Body.Close()

				return "http://" + addr
			}

			if resp != nil {
				_ = resp.Body.Close()
			}
		}

		time.Sleep(50 * time.Millisecond)
	}

	if addr == "" {
		t.Fatalf("management http did not start")
	}

	return "http://" + addr
}

func getJSON(t *testing.T, url string) map[string]any {
	t.Helper()

	resp, err := http.Get(url) //nolint:noctx,gosec
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}

	defer func() { _ = resp.Body.Close() }()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	m := map[string]any{}

	_ = json.Unmarshal(b, &m)

	return m
}
