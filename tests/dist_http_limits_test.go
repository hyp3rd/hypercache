package tests

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// tinyBodyLimit is small enough that any reasonable cache value will
// exceed it — used to trigger fiber's 413 / http.MaxBytesError responses
// in the limit-enforcement tests below.
const tinyBodyLimit = 1024

// TestDistHTTPServer_RejectsOversizedBody verifies the dist HTTP server
// rejects request bodies larger than the configured BodyLimit with HTTP
// 413, instead of buffering an unbounded payload into memory.
func TestDistHTTPServer_RejectsOversizedBody(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	addr := AllocatePort(t)

	bi, err := backend.NewDistMemory(ctx,
		backend.WithDistNode("oversized-server", addr),
		backend.WithDistReplication(1),
		backend.WithDistHTTPLimits(backend.DistHTTPLimits{BodyLimit: tinyBodyLimit}),
	)
	if err != nil {
		t.Fatalf("new dist memory: %v", err)
	}

	dm, ok := bi.(*backend.DistMemory)
	if !ok {
		t.Fatalf("expected *backend.DistMemory, got %T", bi)
	}

	StopOnCleanup(t, dm)

	if !waitForHealth(ctx, "http://"+dm.LocalNodeAddr(), 5*time.Second) {
		t.Fatal("dist HTTP server never came up")
	}

	// Build a request body that comfortably exceeds the limit. The literal
	// JSON envelope adds overhead too, so the value alone is several times
	// the cap — fiber should reject it before reading the whole stream.
	oversizedValue := strings.Repeat("x", 4*tinyBodyLimit)
	body := `{"key":"k","value":"` + oversizedValue + `","expiration":0,"version":1,"origin":"t"}`

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+dm.LocalNodeAddr()+"/internal/set", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 Payload Too Large for oversized body, got %d", resp.StatusCode)
	}
}

// TestDistHTTPClient_RejectsOversizedResponse verifies the dist HTTP
// transport bounds inbound response bodies — a malicious or compromised
// peer cannot OOM the requester via a giant payload.
func TestDistHTTPClient_RejectsOversizedResponse(t *testing.T) {
	t.Parallel()

	// Stub server that always returns a body well past the client cap,
	// regardless of requested path.
	const responseSize = 8 * tinyBodyLimit

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		// Write a JSON-shaped response so the decoder doesn't bail on
		// syntax before hitting the byte cap.
		_, _ = w.Write([]byte(`{"keys":["` + strings.Repeat("x", responseSize) + `"]}`))
	}))
	t.Cleanup(srv.Close)

	transport := backend.NewDistHTTPTransportWithLimits(
		backend.DistHTTPLimits{ResponseLimit: tinyBodyLimit},
		func(_ string) (string, bool) { return srv.URL, true },
	)

	_, err := transport.ListKeys(context.Background(), "ignored")
	if err == nil {
		t.Fatalf("expected ListKeys to fail when response exceeds limit, got nil")
	}

	// http.MaxBytesReader returns *http.MaxBytesError; the transport
	// wraps the decode error so we just check the error chain.
	var maxBytesErr *http.MaxBytesError

	if !errors.As(err, &maxBytesErr) {
		t.Fatalf("expected http.MaxBytesError in error chain, got: %v", err)
	}
}

// TestDistHTTPLimits_DefaultsApply verifies that an option with zero
// fields still produces sane defaults — guards against the partial-override
// contract regressing.
func TestDistHTTPLimits_DefaultsApply(t *testing.T) {
	t.Parallel()

	// Sanity check: end-to-end Set/Get works with no explicit limit option.
	ctx := context.Background()
	addr := AllocatePort(t)

	bi, err := backend.NewDistMemory(ctx,
		backend.WithDistNode("default-limits", addr),
		backend.WithDistReplication(1),
	)
	if err != nil {
		t.Fatalf("new dist memory: %v", err)
	}

	dm, ok := bi.(*backend.DistMemory)
	if !ok {
		t.Fatalf("expected *backend.DistMemory, got %T", bi)
	}

	StopOnCleanup(t, dm)

	if !waitForHealth(ctx, "http://"+dm.LocalNodeAddr(), 5*time.Second) {
		t.Fatal("dist HTTP server never came up")
	}

	err = dm.Set(ctx, &cache.Item{Key: "k", Value: "v", Version: 1, Origin: "t", LastUpdated: time.Now()})
	if err != nil {
		t.Fatalf("default-limits Set should succeed: %v", err)
	}
}

// waitForHealth polls a node's /health endpoint until it returns 200 or
// the deadline elapses. Mirrors the merkle-test waitForMerkleEndpoint
// helper but is local to limits tests so they don't pull in unrelated
// dependencies.
func waitForHealth(ctx context.Context, baseURL string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/health", nil)
		if err != nil {
			return false
		}

		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				return true
			}
		}

		time.Sleep(50 * time.Millisecond)
	}

	return false
}
