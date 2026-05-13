package main

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	fiber "github.com/gofiber/fiber/v3"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/pkg/backend"
)

// newTestServer builds a single-node hypercache + fiber app wired
// with every handler under test. Returned together so test bodies
// can drive the wire (fiber app.Test) without provisioning a real
// listener.
//
// Replication=1 keeps assertions deterministic — no quorum / fan-out
// concerns — and the in-memory backend's lifecycle is tied to t.
func newTestServer(t *testing.T) *fiber.App {
	t.Helper()

	cfg, err := hypercache.NewConfig[backend.DistMemory](constants.DistMemoryBackend)
	if err != nil {
		t.Fatalf("new config: %v", err)
	}

	cfg.DistMemoryOptions = []backend.DistMemoryOption{
		backend.WithDistNode("test-node", "127.0.0.1:0"),
		backend.WithDistReplication(1),
	}

	hc, err := hypercache.New(t.Context(), hypercache.GetDefaultManager(), cfg)
	if err != nil {
		t.Fatalf("new hypercache: %v", err)
	}

	t.Cleanup(func() { _ = hc.Stop(context.Background()) })

	app := fiber.New()
	nodeCtx := &nodeContext{hc: hc, nodeID: "test-node"}

	// Match production ordering: literal /v1/cache/keys before the
	// parameterized /v1/cache/:key so the router picks handleListKeys
	// for the literal path.
	app.Get("/v1/cache/keys", func(c fiber.Ctx) error { return handleListKeys(c, nodeCtx) })
	app.Get("/v1/cache/:key", func(c fiber.Ctx) error { return handleGet(c, nodeCtx) })
	app.Head("/v1/cache/:key", func(c fiber.Ctx) error { return handleHead(c, nodeCtx) })
	app.Put("/v1/cache/:key", func(c fiber.Ctx) error { return handlePut(c, nodeCtx) })
	app.Delete("/v1/cache/:key", func(c fiber.Ctx) error { return handleDelete(c, nodeCtx) })
	app.Post("/v1/cache/batch/get", func(c fiber.Ctx) error { return handleBatchGet(c, nodeCtx) })
	app.Post("/v1/cache/batch/put", func(c fiber.Ctx) error { return handleBatchPut(c, nodeCtx) })
	app.Post("/v1/cache/batch/delete", func(c fiber.Ctx) error { return handleBatchDelete(c, nodeCtx) })

	return app
}

// doRequest is a small wrapper around fiber's in-memory test
// transport. Returns status + body string + Content-Type so each
// test only has to think about the assertion at hand.
type doResult struct {
	status      int
	body        string
	contentType string
	headers     http.Header
}

func doRequest(t *testing.T, app *fiber.App, method, target, body string, headers map[string]string) doResult {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))

	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test %s %s: %v", method, target, err)
	}

	defer func() { _ = resp.Body.Close() }()

	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		t.Fatalf("read body: %v", readErr)
	}

	return doResult{
		status:      resp.StatusCode,
		body:        string(respBody),
		contentType: resp.Header.Get(fiber.HeaderContentType),
		headers:     resp.Header,
	}
}

// TestHandleHead_PresentAndMissing pins the HEAD contract: 200 +
// X-Cache-* headers when the key exists, 404 with no headers when
// not. Header set must include version + node identity so cache
// revalidation flows have everything they need without a body
// transfer.
func TestHandleHead_PresentAndMissing(t *testing.T) {
	t.Parallel()

	app := newTestServer(t)

	// Seed a key with a TTL.
	put := doRequest(t, app, http.MethodPut, "/v1/cache/k?ttl=30s", "world", nil)
	if put.status != http.StatusOK {
		t.Fatalf("put: %d", put.status)
	}

	head := doRequest(t, app, http.MethodHead, "/v1/cache/k", "", nil)
	if head.status != http.StatusOK {
		t.Fatalf("HEAD present: status %d", head.status)
	}

	if head.headers.Get("X-Cache-Version") == "" {
		t.Fatal("HEAD response missing X-Cache-Version header")
	}

	if head.headers.Get("X-Cache-Node") != "test-node" {
		t.Fatalf("X-Cache-Node = %q, want test-node", head.headers.Get("X-Cache-Node"))
	}

	if head.headers.Get("X-Cache-Ttl-Ms") == "" {
		t.Fatal("HEAD with TTL missing X-Cache-Ttl-Ms header")
	}

	miss := doRequest(t, app, http.MethodHead, "/v1/cache/never", "", nil)
	if miss.status != http.StatusNotFound {
		t.Fatalf("HEAD missing: status %d, want 404", miss.status)
	}
}

// TestHandleGet_AcceptJSONReturnsEnvelope pins the
// response-consistency contract: a GET with `Accept:
// application/json` returns the itemEnvelope shape with TTL,
// version, owners, and a base64 value — same shape as a
// batch-get result.
func TestHandleGet_AcceptJSONReturnsEnvelope(t *testing.T) {
	t.Parallel()

	app := newTestServer(t)

	put := doRequest(t, app, http.MethodPut, "/v1/cache/k?ttl=30s", "world", nil)
	if put.status != http.StatusOK {
		t.Fatalf("put: %d", put.status)
	}

	got := doRequest(t, app, http.MethodGet, "/v1/cache/k", "", map[string]string{
		fiber.HeaderAccept: fiber.MIMEApplicationJSON,
	})
	if got.status != http.StatusOK {
		t.Fatalf("GET: status %d", got.status)
	}

	if !strings.Contains(got.contentType, "json") {
		t.Fatalf("content-type = %q, want application/json", got.contentType)
	}

	var env itemEnvelope

	err := json.Unmarshal([]byte(got.body), &env)
	if err != nil {
		t.Fatalf("decode envelope: %v; body=%s", err, got.body)
	}

	if env.Key != "k" {
		t.Errorf("key = %q, want k", env.Key)
	}

	if env.ValueEncoding != "base64" {
		t.Errorf("value_encoding = %q, want base64", env.ValueEncoding)
	}

	decoded, decodeErr := base64.StdEncoding.DecodeString(env.Value)
	if decodeErr != nil || string(decoded) != "world" {
		t.Errorf("value decoded = %q (err=%v), want world", decoded, decodeErr)
	}

	if env.TTLMs <= 0 || env.TTLMs > 30_000 {
		t.Errorf("ttl_ms = %d, want (0, 30000]", env.TTLMs)
	}

	if env.Version == 0 {
		t.Error("version must be > 0 after a write")
	}
}

// TestHandleGet_DefaultIsRawBytes pins the back-compat contract:
// without an Accept header, GET returns raw bytes — operators
// using bare `curl` keep seeing the literal value.
func TestHandleGet_DefaultIsRawBytes(t *testing.T) {
	t.Parallel()

	app := newTestServer(t)

	put := doRequest(t, app, http.MethodPut, "/v1/cache/k", "hello", nil)
	if put.status != http.StatusOK {
		t.Fatalf("put: %d", put.status)
	}

	got := doRequest(t, app, http.MethodGet, "/v1/cache/k", "", nil)
	if got.body != "hello" {
		t.Fatalf("body = %q, want hello", got.body)
	}

	if !strings.Contains(got.contentType, "octet-stream") {
		t.Fatalf("content-type = %q, want octet-stream", got.contentType)
	}
}

// TestHandleBatchPut_MixedEncodings pins the batch-put contract:
// items can be UTF-8 strings (default) or base64-encoded bytes
// via value_encoding. Per-item errors are surfaced without
// failing the whole batch.
func TestHandleBatchPut_MixedEncodings(t *testing.T) {
	t.Parallel()

	app := newTestServer(t)

	body := `{
		"items": [
			{"key": "k1", "value": "hello", "ttl_ms": 30000},
			{"key": "k2", "value": "d29ybGQ=", "value_encoding": "base64"},
			{"key": "", "value": "rejected"}
		]
	}`

	got := doRequest(t, app, http.MethodPost, "/v1/cache/batch/put", body, nil)
	if got.status != http.StatusOK {
		t.Fatalf("batch-put: status %d, body=%s", got.status, got.body)
	}

	var resp batchPutResponse

	err := json.Unmarshal([]byte(got.body), &resp)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(resp.Results) != 3 {
		t.Fatalf("got %d results, want 3", len(resp.Results))
	}

	if !resp.Results[0].Stored || resp.Results[0].Bytes != 5 {
		t.Errorf("k1 result = %+v", resp.Results[0])
	}

	if !resp.Results[1].Stored || resp.Results[1].Bytes != 5 {
		t.Errorf("k2 result = %+v", resp.Results[1])
	}

	if resp.Results[2].Stored || resp.Results[2].Code != codeBadRequest {
		t.Errorf("empty-key result must be rejected: %+v", resp.Results[2])
	}
}

// TestHandleBatchGet_FoundAndMissing pins the batch-get contract:
// each requested key returns its own result entry; missing keys
// produce found:false rather than failing the whole batch.
// Found entries carry the same metadata shape as
// itemEnvelope — verified by checking the value round-trips
// from base64 back to the original.
func TestHandleBatchGet_FoundAndMissing(t *testing.T) {
	t.Parallel()

	app := newTestServer(t)

	put := doRequest(t, app, http.MethodPut, "/v1/cache/k1", "alpha", nil)
	if put.status != http.StatusOK {
		t.Fatalf("seed put: %d", put.status)
	}

	body := `{"keys": ["k1", "missing", "k1"]}`

	got := doRequest(t, app, http.MethodPost, "/v1/cache/batch/get", body, nil)
	if got.status != http.StatusOK {
		t.Fatalf("batch-get: status %d", got.status)
	}

	var resp batchGetResponse

	err := json.Unmarshal([]byte(got.body), &resp)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(resp.Results) != 3 {
		t.Fatalf("got %d results, want 3", len(resp.Results))
	}

	if !resp.Results[0].Found {
		t.Errorf("k1 should be found: %+v", resp.Results[0])
	}

	decoded, decodeErr := base64.StdEncoding.DecodeString(resp.Results[0].Value)
	if decodeErr != nil || string(decoded) != "alpha" {
		t.Errorf("k1 decoded = %q (err=%v), want alpha", decoded, decodeErr)
	}

	if resp.Results[1].Found {
		t.Errorf("missing key must be found:false: %+v", resp.Results[1])
	}

	// Duplicate request — returns the same result twice; pins that
	// the iteration is per-key, not deduped.
	if !resp.Results[2].Found || resp.Results[2].Key != "k1" {
		t.Errorf("duplicate-k1 result = %+v", resp.Results[2])
	}
}

// TestHandleBatchDelete_BasicFlow seeds a key, deletes it via
// batch, and asserts the post-delete batch-get reports it
// missing.
func TestHandleBatchDelete_BasicFlow(t *testing.T) {
	t.Parallel()

	app := newTestServer(t)

	put := doRequest(t, app, http.MethodPut, "/v1/cache/k", "v", nil)
	if put.status != http.StatusOK {
		t.Fatalf("put: %d", put.status)
	}

	del := doRequest(t, app, http.MethodPost, "/v1/cache/batch/delete", `{"keys":["k"]}`, nil)
	if del.status != http.StatusOK {
		t.Fatalf("batch-delete: status %d", del.status)
	}

	var resp batchDeleteResponse

	err := json.Unmarshal([]byte(del.body), &resp)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(resp.Results) != 1 || !resp.Results[0].Deleted {
		t.Fatalf("expected one deleted result; got %+v", resp.Results)
	}

	got := doRequest(t, app, http.MethodPost, "/v1/cache/batch/get", `{"keys":["k"]}`, nil)
	if !strings.Contains(got.body, `"found":false`) {
		t.Fatalf("batch-get post-delete should report found:false; got %s", got.body)
	}
}

// seedListKeysFixture seeds `count` keys prefixed `first-NN` plus a
// `second-1` decoy via PUT. Returns the test app so the caller can
// drive the list-keys endpoint against the populated cache. Kept
// here rather than in newTestServer so the test bodies stay
// declarative.
func seedListKeysFixture(t *testing.T, count int) *fiber.App {
	t.Helper()

	app := newTestServer(t)

	for i := range count {
		n := strconv.Itoa(i + 1)
		key := "first-" + strings.Repeat("0", 2-len(n)) + n

		put := doRequest(t, app, http.MethodPut, "/v1/cache/"+key, "v", nil)
		if put.status != http.StatusOK {
			t.Fatalf("seed put %s: %d", key, put.status)
		}
	}

	put := doRequest(t, app, http.MethodPut, "/v1/cache/second-1", "v", nil)
	if put.status != http.StatusOK {
		t.Fatalf("seed second put: %d", put.status)
	}

	return app
}

// fetchListKeysPage drives one /v1/cache/keys request and decodes
// the response, failing the test on transport or parse errors.
// Extracted so the test body can focus on the cursor walk.
func fetchListKeysPage(t *testing.T, app *fiber.App, cursor string) listKeysResponse {
	t.Helper()

	target := "/v1/cache/keys?q=first-&limit=10"
	if cursor != "" {
		target += "&cursor=" + cursor
	}

	got := doRequest(t, app, http.MethodGet, target, "", nil)
	if got.status != http.StatusOK {
		t.Fatalf("cursor=%q: status %d body=%s", cursor, got.status, got.body)
	}

	var resp listKeysResponse

	err := json.Unmarshal([]byte(got.body), &resp)
	if err != nil {
		t.Fatalf("cursor=%q decode: %v", cursor, err)
	}

	return resp
}

// TestHandleListKeys_PrefixAndPaging drives the v1 list-keys
// endpoint end-to-end: seed via PUT, filter by prefix, walk the
// cursor across multiple pages, assert the union matches the seed
// set and no key appears twice.
func TestHandleListKeys_PrefixAndPaging(t *testing.T) {
	t.Parallel()

	const seedCount = 25

	app := seedListKeysFixture(t, seedCount)

	collected := make(map[string]struct{}, seedCount)
	cursor := ""

	for range 10 {
		resp := fetchListKeysPage(t, app, cursor)

		if resp.TotalMatched != seedCount {
			t.Fatalf("total_matched=%d, want %d", resp.TotalMatched, seedCount)
		}

		for _, k := range resp.Keys {
			if !strings.HasPrefix(k, "first-") {
				t.Fatalf("non-prefix key in result: %s", k)
			}

			if _, dup := collected[k]; dup {
				t.Fatalf("duplicate key across pages: %s", k)
			}

			collected[k] = struct{}{}
		}

		if resp.NextCursor == "" {
			break
		}

		cursor = resp.NextCursor
	}

	if len(collected) != seedCount {
		t.Fatalf("collected %d keys across pages, want %d", len(collected), seedCount)
	}
}

// TestHandleListKeys_InvalidCursor pins that a malformed cursor
// surfaces 400, not 500 — the cursor field is operator-controlled
// and must be validated at the boundary.
func TestHandleListKeys_InvalidCursor(t *testing.T) {
	t.Parallel()

	app := newTestServer(t)

	got := doRequest(t, app, http.MethodGet, "/v1/cache/keys?cursor=not-a-number", "", nil)
	if got.status != http.StatusBadRequest {
		t.Fatalf("expected 400 for malformed cursor, got %d body=%s", got.status, got.body)
	}
}

// TestHandleListKeys_InvalidGlob surfaces malformed glob patterns
// as 400, matching the same validate-at-boundary contract as
// cursor.
func TestHandleListKeys_InvalidGlob(t *testing.T) {
	t.Parallel()

	app := newTestServer(t)

	// "[unclosed" is a malformed character class — URL-encoded so
	// the literal `[` is preserved through fiber's query parser.
	target := "/v1/cache/keys?q=" + url.QueryEscape("[unclosed")

	got := doRequest(t, app, http.MethodGet, target, "", nil)
	if got.status != http.StatusBadRequest {
		t.Fatalf("expected 400 for malformed glob, got %d body=%s", got.status, got.body)
	}
}
