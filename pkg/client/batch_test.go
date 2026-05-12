package client_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/pkg/client"
)

// batchStubNode is the canonical node ID the batch fixtures
// report back. Mirrors the single-key stubNode constant in
// client_test.go but kept distinct so we can grep batch failures
// in isolation.
const batchStubNode = "batch-stub-node"

// --- BatchSet ---

// TestClient_BatchSet_Success drives BatchSet's happy path: every
// item is stored, each per-item result has Err nil and a populated
// Owners list, and the order of returned results matches the order
// of input items.
func TestClient_BatchSet_Success(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(batchSetSuccessHandler))
	t.Cleanup(srv.Close)

	c, _ := client.New([]string{srv.URL})
	t.Cleanup(func() { _ = c.Close() })

	results, err := c.BatchSet(context.Background(), []client.BatchSetItem{
		{Key: "k1", Value: []byte("one"), TTL: time.Minute},
		{Key: "k2", Value: []byte("two"), TTL: time.Minute},
		{Key: "k3", Value: []byte("three")},
	})
	if err != nil {
		t.Fatalf("BatchSet: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("results length: got %d, want 3", len(results))
	}

	for i, r := range results {
		if !r.Stored {
			t.Errorf("results[%d] (%s): not stored", i, r.Key)
		}

		if r.Err != nil {
			t.Errorf("results[%d] (%s): unexpected err %v", i, r.Key, r.Err)
		}
	}

	if results[0].Bytes != len("one") {
		t.Errorf("results[0].Bytes: got %d, want 3", results[0].Bytes)
	}
}

// TestClient_BatchSet_PerItemFailure pins that per-item failures
// surface via the result's Err field (a *StatusError matching the
// canonical sentinels), while the overall call returns nil error
// because the HTTP request itself succeeded.
func TestClient_BatchSet_PerItemFailure(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		_ = json.NewEncoder(w).Encode(map[string]any{
			jsonResults: []map[string]any{
				{jsonKey: "ok", jsonStored: true, "bytes": 4, jsonOwners: []string{batchStubNode}},
				{jsonKey: "drain", jsonStored: false, "code": "DRAINING", "error": "node draining"},
			},
			jsonNode: batchStubNode,
		})
	}))

	t.Cleanup(srv.Close)

	c, _ := client.New([]string{srv.URL})
	t.Cleanup(func() { _ = c.Close() })

	results, err := c.BatchSet(context.Background(), []client.BatchSetItem{
		{Key: "ok", Value: []byte("good")},
		{Key: "drain", Value: []byte("bad")},
	})
	if err != nil {
		t.Fatalf("BatchSet: %v (HTTP-level call must not fail)", err)
	}

	if !results[0].Stored || results[0].Err != nil {
		t.Errorf("results[0]: want stored, got %+v", results[0])
	}

	if results[1].Stored {
		t.Errorf("results[1]: want stored=false; got true")
	}

	if !errors.Is(results[1].Err, client.ErrDraining) {
		t.Errorf("results[1].Err: want errors.Is(_, ErrDraining); got %v", results[1].Err)
	}
}

// TestClient_BatchSet_Empty pins the no-op contract: an empty
// input slice returns an empty result slice and nil error WITHOUT
// dispatching an HTTP request (the server's batch handler would
// 400 on an empty items array, but we short-circuit client-side).
func TestClient_BatchSet_Empty(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be hit on empty BatchSet input")
	}))

	t.Cleanup(srv.Close)

	c, _ := client.New([]string{srv.URL})
	t.Cleanup(func() { _ = c.Close() })

	results, err := c.BatchSet(context.Background(), nil)
	if err != nil {
		t.Fatalf("BatchSet nil: %v", err)
	}

	if len(results) != 0 {
		t.Errorf("BatchSet nil: want empty slice, got %v", results)
	}
}

// --- BatchGet ---

// TestClient_BatchGet_MixedFoundMissing covers the canonical case:
// some keys exist, some don't. Each result carries the Found flag;
// Item is populated only when Found=true and carries the value
// decoded from base64 back to raw bytes.
func TestClient_BatchGet_MixedFoundMissing(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(batchGetMixedHandler))
	t.Cleanup(srv.Close)

	c, _ := client.New([]string{srv.URL})
	t.Cleanup(func() { _ = c.Close() })

	results, err := c.BatchGet(context.Background(), []string{"a", "missing", "b"})
	if err != nil {
		t.Fatalf("BatchGet: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("results length: got %d, want 3", len(results))
	}

	// "a" found
	if !results[0].Found || results[0].Item == nil {
		t.Fatalf("results[0] (a): want found, got %+v", results[0])
	}

	if string(results[0].Item.Value) != "value-a" {
		t.Errorf("results[0].Item.Value: got %q, want value-a", results[0].Item.Value)
	}

	if results[0].Item.Version != 7 {
		t.Errorf("results[0].Item.Version: got %d, want 7", results[0].Item.Version)
	}

	if results[0].Item.Node != batchStubNode {
		t.Errorf("results[0].Item.Node: got %q, want %s", results[0].Item.Node, batchStubNode)
	}

	// "missing" not found
	if results[1].Found || results[1].Item != nil {
		t.Errorf("results[1] (missing): want !found, got %+v", results[1])
	}

	// "b" found
	if !results[2].Found || string(results[2].Item.Value) != "value-b" {
		t.Errorf("results[2] (b): want found with value-b, got %+v", results[2])
	}
}

// TestClient_BatchGet_Empty pins the empty-input no-op shape.
func TestClient_BatchGet_Empty(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be hit on empty BatchGet input")
	}))

	t.Cleanup(srv.Close)

	c, _ := client.New([]string{srv.URL})
	t.Cleanup(func() { _ = c.Close() })

	results, err := c.BatchGet(context.Background(), nil)
	if err != nil {
		t.Fatalf("BatchGet nil: %v", err)
	}

	if len(results) != 0 {
		t.Errorf("BatchGet nil: want empty slice, got %v", results)
	}
}

// --- BatchDelete ---

// TestClient_BatchDelete_Success drives the happy path; every key
// is deleted idempotently. Per-item Err must stay nil.
func TestClient_BatchDelete_Success(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/cache/batch/delete" {
			http.Error(w, "wrong route", http.StatusNotFound)

			return
		}

		var req struct {
			Keys []string `json:"keys"`
		}

		_ = json.NewDecoder(r.Body).Decode(&req)

		results := make([]map[string]any, 0, len(req.Keys))
		for _, k := range req.Keys {
			results = append(results, map[string]any{
				jsonKey: k, "deleted": true,
				jsonOwners: []string{batchStubNode},
			})
		}

		w.Header().Set("Content-Type", "application/json")

		_ = json.NewEncoder(w).Encode(map[string]any{
			jsonResults: results,
			jsonNode:    batchStubNode,
		})
	}))

	t.Cleanup(srv.Close)

	c, _ := client.New([]string{srv.URL})
	t.Cleanup(func() { _ = c.Close() })

	results, err := c.BatchDelete(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("BatchDelete: %v", err)
	}

	for i, r := range results {
		if !r.Deleted || r.Err != nil {
			t.Errorf("results[%d] (%s): not deleted: %+v", i, r.Key, r)
		}
	}
}

// TestClient_BatchDelete_PerItemFailure pins the cluster-draining
// case mid-batch: the call still returns nil error, but the
// affected items carry an Err matching ErrDraining.
func TestClient_BatchDelete_PerItemFailure(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		_ = json.NewEncoder(w).Encode(map[string]any{
			jsonResults: []map[string]any{
				{jsonKey: "ok", "deleted": true, jsonOwners: []string{batchStubNode}},
				{jsonKey: "drain", "deleted": false, "code": "DRAINING", "error": "node draining"},
			},
			jsonNode: batchStubNode,
		})
	}))

	t.Cleanup(srv.Close)

	c, _ := client.New([]string{srv.URL})
	t.Cleanup(func() { _ = c.Close() })

	results, err := c.BatchDelete(context.Background(), []string{"ok", "drain"})
	if err != nil {
		t.Fatalf("BatchDelete: %v", err)
	}

	if !errors.Is(results[1].Err, client.ErrDraining) {
		t.Errorf("results[1].Err: want errors.Is(_, ErrDraining); got %v", results[1].Err)
	}
}

// --- HTTP-level failure ---

// TestClient_Batch_HTTPLevelFailureSurfacesError pins what happens
// when the batch endpoint itself returns 5xx (vs the per-item
// failure mode above). The method returns a non-nil error
// satisfying errors.Is(err, client.ErrInternal); no per-item
// results come back.
func TestClient_Batch_HTTPLevelFailureSurfacesError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body, _ := json.Marshal(map[string]string{
			"code": "INTERNAL", "error": "the world is on fire",
		})

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)

		_, _ = w.Write(body)
	}))

	t.Cleanup(srv.Close)

	c, _ := client.New([]string{srv.URL})
	t.Cleanup(func() { _ = c.Close() })

	_, err := c.BatchSet(context.Background(), []client.BatchSetItem{
		{Key: "k", Value: []byte("v")},
	})

	// 5xx + only one endpoint configured → wrapped with ErrAllEndpointsFailed.
	if !errors.Is(err, client.ErrAllEndpointsFailed) {
		t.Errorf("want ErrAllEndpointsFailed, got %v", err)
	}

	if !errors.Is(err, client.ErrInternal) {
		t.Errorf("want errors.Is(_, ErrInternal); got %v", err)
	}
}

// --- fixture helpers ---

// batchSetSuccessHandler answers POST /v1/cache/batch/put with one
// stored=true result per input item, base64-decoding the wire value
// to verify the SDK is sending the right shape. Extracted from the
// test body so the test function stays under the cognitive-
// complexity cap.
func batchSetSuccessHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/cache/batch/put" || r.Method != http.MethodPost {
		http.Error(w, "wrong route", http.StatusNotFound)

		return
	}

	var req struct {
		Items []struct {
			Key           string `json:"key"`
			Value         string `json:"value"`
			ValueEncoding string `json:"value_encoding"`
			TTLMs         int64  `json:"ttl_ms"`
		} `json:"items"`
	}

	_ = json.NewDecoder(r.Body).Decode(&req)

	results := make([]map[string]any, 0, len(req.Items))
	for _, it := range req.Items {
		decoded, decodeErr := base64.StdEncoding.DecodeString(it.Value)
		if decodeErr != nil {
			results = append(results, map[string]any{
				jsonKey: it.Key, jsonStored: false,
				"code": "BAD_REQUEST", "error": "value not base64",
			})

			continue
		}

		results = append(results, map[string]any{
			jsonKey: it.Key, jsonStored: true,
			"bytes":    len(decoded),
			jsonOwners: []string{batchStubNode},
		})
	}

	w.Header().Set("Content-Type", "application/json")

	body, err := json.Marshal(map[string]any{
		jsonResults: results,
		jsonNode:    batchStubNode,
	})
	if err != nil {
		http.Error(w, "marshal: "+err.Error(), http.StatusInternalServerError)

		return
	}

	_, _ = w.Write(body)
}

// batchGetMixedHandler answers POST /v1/cache/batch/get with
// per-key results. Keys named "missing" return found=false; all
// other keys return a base64-encoded "value-<key>" with version=7.
func batchGetMixedHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/cache/batch/get" {
		http.Error(w, "wrong route", http.StatusNotFound)

		return
	}

	var req struct {
		Keys []string `json:"keys"`
	}

	_ = json.NewDecoder(r.Body).Decode(&req)

	results := make([]map[string]any, 0, len(req.Keys))

	for _, k := range req.Keys {
		if k == "missing" {
			results = append(results, map[string]any{jsonKey: k, "found": false})

			continue
		}

		results = append(results, map[string]any{
			jsonKey:          k,
			"found":          true,
			"value":          base64.StdEncoding.EncodeToString([]byte("value-" + k)),
			"value_encoding": "base64",
			"version":        7,
			jsonOwners:       []string{batchStubNode},
		})
	}

	w.Header().Set("Content-Type", "application/json")

	body, err := json.Marshal(map[string]any{
		jsonResults: results,
		jsonNode:    batchStubNode,
	})
	if err != nil {
		http.Error(w, "marshal: "+err.Error(), http.StatusInternalServerError)

		return
	}

	_, _ = w.Write(body)
}
