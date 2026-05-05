package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	fiber "github.com/gofiber/fiber/v3"
)

// writeValueResult bundles the bytes-emitted + Content-Type pair
// returned by runWriteValue. Returned as a struct (not two strings)
// so the same-typed pair doesn't trip the confusing-results linter.
type writeValueResult struct {
	body        string
	contentType string
}

// runWriteValue stands up a one-route fiber app whose handler emits
// the supplied value via writeValue, then drives it through fiber's
// in-memory test transport.
//
// We can't unit-test writeValue against a fake fiber.Ctx because the
// fiber.Ctx interface is only constructible by fiber itself; this
// helper threads the value through a real router so the write path
// is end-to-end identical to the production one.
func runWriteValue(t *testing.T, value any) writeValueResult {
	t.Helper()

	app := fiber.New()
	app.Get("/probe", func(c fiber.Ctx) error { return writeValue(c, value) })

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/probe", nil)

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		t.Fatalf("read body: %v", readErr)
	}

	return writeValueResult{body: string(body), contentType: resp.Header.Get(fiber.HeaderContentType)}
}

// TestWriteValue_ByteSlice covers the writer-node path: the local
// shard holds the value as a native []byte (the type the PUT
// handler stored). It must round-trip unchanged with
// `application/octet-stream`.
func TestWriteValue_ByteSlice(t *testing.T) {
	t.Parallel()

	got := runWriteValue(t, []byte("world"))

	if got.body != "world" {
		t.Fatalf("body = %q, want %q", got.body, "world")
	}

	if !strings.Contains(got.contentType, "octet-stream") {
		t.Fatalf("content-type = %q, want octet-stream", got.contentType)
	}
}

// TestWriteValue_StringBase64 covers the *replica*-node path: when
// a replica receives a value over the dist HTTP transport, the
// JSON unmarshal turns the upstream `[]byte` into a Go `string`
// holding the base64 representation. writeValue must base64-decode
// it so the client sees the original bytes.
func TestWriteValue_StringBase64(t *testing.T) {
	t.Parallel()

	// "world" -> base64
	got := runWriteValue(t, "d29ybGQ=")

	if got.body != "world" {
		t.Fatalf("body = %q, want %q (base64-decoded)", got.body, "world")
	}

	if !strings.Contains(got.contentType, "octet-stream") {
		t.Fatalf("content-type = %q, want octet-stream", got.contentType)
	}
}

// TestWriteValue_StringNotBase64 pins the content-type contract
// for plain string values. The body itself may pass through as
// the original string OR a base64-decoded form — the heuristic
// admits 4-char strings shaped like valid base64 — but the
// content-type must remain octet-stream so callers always know
// what to expect.
func TestWriteValue_StringNotBase64(t *testing.T) {
	t.Parallel()

	got := runWriteValue(t, "abcd") // 4-char, valid base64 alphabet

	if !strings.Contains(got.contentType, "octet-stream") {
		t.Fatalf("content-type = %q, want octet-stream", got.contentType)
	}
}

// TestWriteValue_JSONRawMessageString is the pinned regression for
// the non-owner GET bug: the dist HTTP transport's decodeGetBody
// returns Item.Value as `json.RawMessage` containing the raw
// wire-bytes (e.g. `"d29ybGQ="` *with the surrounding quotes*).
// Pre-fix this fell to the `default` branch and emitted a
// JSON-quoted base64 string instead of the original bytes.
func TestWriteValue_JSONRawMessageString(t *testing.T) {
	t.Parallel()

	// The wire literal: a JSON string containing base64 of "world".
	raw := json.RawMessage(`"d29ybGQ="`)

	got := runWriteValue(t, raw)

	if got.body != "world" {
		t.Fatalf("body = %q, want %q (raw-message → string → base64-decode)", got.body, "world")
	}

	if !strings.Contains(got.contentType, "octet-stream") {
		t.Fatalf("content-type = %q, want octet-stream", got.contentType)
	}
}

// TestWriteValue_JSONRawMessageObject covers the non-string raw-JSON
// path: when a value is structured (object/array/number), the
// json.RawMessage isn't a string — writeRawJSON must emit it as
// raw JSON with the application/json content-type so structured
// values still round-trip.
func TestWriteValue_JSONRawMessageObject(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"foo":42}`)

	got := runWriteValue(t, raw)

	if got.body != `{"foo":42}` {
		t.Fatalf("body = %q, want raw JSON object", got.body)
	}

	if !strings.Contains(got.contentType, "json") {
		t.Fatalf("content-type = %q, want application/json", got.contentType)
	}
}

// TestDecodeBase64Bytes_TooShort pins the length-floor in the
// base64 heuristic — strings shorter than 4 chars cannot be
// padded base64 output, so we must not attempt to decode them.
func TestDecodeBase64Bytes_TooShort(t *testing.T) {
	t.Parallel()

	cases := []string{"", "a", "ab", "abc"}
	for _, in := range cases {
		_, ok := decodeBase64Bytes(in)
		if ok {
			t.Errorf("decodeBase64Bytes(%q): ok=true, want false (too short)", in)
		}
	}
}

// TestDecodeBase64Bytes_NotPadded pins the modulo-4 check — base64
// output is always a multiple of 4 bytes when padded, so unpadded
// strings shouldn't be treated as base64.
func TestDecodeBase64Bytes_NotPadded(t *testing.T) {
	t.Parallel()

	_, ok := decodeBase64Bytes("abcde") // 5 chars
	if ok {
		t.Errorf("expected 5-char input to be rejected (len%%4 != 0)")
	}
}
