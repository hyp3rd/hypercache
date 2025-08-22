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

// TestManagementHTTPDistMemory validates management endpoints for the experimental DistMemory backend.
func TestManagementHTTPDistMemory(t *testing.T) { //nolint:paralleltest
	cfg := hypercache.NewConfig[backend.DistMemory](constants.DistMemoryBackend)

	cfg.HyperCacheOptions = append(cfg.HyperCacheOptions,
		hypercache.WithManagementHTTP[backend.DistMemory]("127.0.0.1:0"), // ephemeral port
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

	// Insert a key to exercise owners endpoint.
	err = hc.Set(context.Background(), "alpha", "value", 0)
	if err != nil {
		// not fatal for owners shape but should succeed given replication=1
		t.Fatalf("set alpha: %v", err)
	}

	// /config should include replication + virtualNodesPerNode
	configBody := getJSON(t, baseURL+"/config")
	if _, ok := configBody["replication"]; !ok {
		t.Errorf("/config missing replication")
	}

	if vnp, ok := configBody["virtualNodesPerNode"]; !ok || vnp == nil {
		t.Errorf("/config missing virtualNodesPerNode")
	}

	// /dist/metrics basic shape
	metricsBody := getJSON(t, baseURL+"/dist/metrics")
	if _, ok := metricsBody["ForwardGet"]; !ok { // one exported field
		// could be 404 if backend unsupported (should not be here)
		if e, hasErr := metricsBody["error"]; hasErr {
			t.Fatalf("/dist/metrics returned error: %v", e)
		}
		// else fail
		t.Errorf("/dist/metrics missing ForwardGet field")
	}

	// /dist/owners
	ownersBody := getJSON(t, baseURL+"/dist/owners?key=alpha")
	if _, ok := ownersBody["owners"]; !ok {
		if e, hasErr := ownersBody["error"]; hasErr {
			t.Fatalf("/dist/owners returned error: %v", e)
		}

		t.Errorf("/dist/owners missing owners field")
	}

	// /cluster/members
	membersBody := getJSON(t, baseURL+"/cluster/members")
	if _, ok := membersBody["members"]; !ok {
		if e, hasErr := membersBody["error"]; hasErr {
			t.Fatalf("/cluster/members returned error: %v", e)
		}

		t.Errorf("/cluster/members missing members field")
	}

	// /cluster/ring
	ringBody := getJSON(t, baseURL+"/cluster/ring")
	if _, ok := ringBody["vnodes"]; !ok {
		if e, hasErr := ringBody["error"]; hasErr {
			t.Fatalf("/cluster/ring returned error: %v", e)
		}

		t.Errorf("/cluster/ring missing vnodes field")
	}
}

// waitForMgmt waits until management HTTP server is bound and responsive.
func waitForMgmt(t *testing.T, hc *hypercache.HyperCache[backend.DistMemory]) string { //nolint:thelper
	deadline := time.Now().Add(2 * time.Second)

	var addr string
	for time.Now().Before(deadline) {
		addr = hc.ManagementHTTPAddress()
		if addr != "" {
			resp, err := http.Get("http://" + addr + "/health") //nolint:noctx,gosec
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

func getJSON(t *testing.T, url string) map[string]any { //nolint:thelper
	resp, err := http.Get(url) //nolint:noctx,gosec
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	m := map[string]any{}

	_ = json.Unmarshal(b, &m)

	return m
}
