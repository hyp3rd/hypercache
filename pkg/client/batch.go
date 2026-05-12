package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// BatchSetItem is one entry in a BatchSet call. Each item carries
// its own TTL so callers can mix expiring and non-expiring writes
// in a single batch — TTL <= 0 means no expiry, matching Set's
// single-key semantics.
type BatchSetItem struct {
	Key   string
	Value []byte
	TTL   time.Duration
}

// BatchGetResult is the per-key result of a BatchGet call. Found
// flags whether the key existed; Item carries the full envelope
// when Found is true and is nil otherwise. The HTTP request itself
// can succeed even when some keys are missing — partial misses are
// the normal shape of a bulk-read call.
type BatchGetResult struct {
	Key   string
	Found bool
	Item  *Item
}

// BatchPutResult is the per-key result of a BatchSet call. Stored
// flags whether the item was written; Bytes / Owners are populated
// on success. Err is non-nil when the per-item write failed (e.g.
// the cluster was draining for that key's primary) and matches the
// SDK's standard *StatusError shape so callers can errors.Is /
// errors.As against the failure mode.
type BatchPutResult struct {
	Key    string
	Stored bool
	Bytes  int
	Owners []string
	Err    *StatusError
}

// BatchDeleteResult is the per-key result of a BatchDelete call.
// Deleted flags whether the item was removed; on the cache's
// idempotent-delete semantics, deleting a missing key is still
// reported as Deleted=true (it's idempotent in REST terms — the
// post-state is "key does not exist"). Err is non-nil only when
// the cluster could not service the delete at all.
type BatchDeleteResult struct {
	Key     string
	Deleted bool
	Owners  []string
	Err     *StatusError
}

// --- wire shapes ---

type batchGetRequest struct {
	Keys []string `json:"keys"`
}

type batchGetResultWire struct {
	Key           string   `json:"key"`
	Found         bool     `json:"found"`
	Value         string   `json:"value,omitempty"`
	ValueEncoding string   `json:"value_encoding,omitempty"`
	TTLMs         int64    `json:"ttl_ms,omitempty"`
	ExpiresAt     string   `json:"expires_at,omitempty"`
	Version       uint64   `json:"version,omitempty"`
	Origin        string   `json:"origin,omitempty"`
	LastUpdated   string   `json:"last_updated,omitempty"`
	Owners        []string `json:"owners,omitempty"`
}

type batchGetResponse struct {
	Results []batchGetResultWire `json:"results"`
	Node    string               `json:"node"`
}

type batchPutItemWire struct {
	Key           string `json:"key"`
	Value         string `json:"value"`
	ValueEncoding string `json:"value_encoding,omitempty"`
	TTLMs         int64  `json:"ttl_ms,omitempty"`
}

type batchPutRequest struct {
	Items []batchPutItemWire `json:"items"`
}

type batchPutResultWire struct {
	Key    string   `json:"key"`
	Stored bool     `json:"stored"`
	Bytes  int      `json:"bytes,omitempty"`
	Owners []string `json:"owners,omitempty"`
	Error  string   `json:"error,omitempty"`
	Code   string   `json:"code,omitempty"`
}

type batchPutResponse struct {
	Results []batchPutResultWire `json:"results"`
	Node    string               `json:"node"`
}

type batchDeleteRequest struct {
	Keys []string `json:"keys"`
}

type batchDeleteResultWire struct {
	Key     string   `json:"key"`
	Deleted bool     `json:"deleted"`
	Owners  []string `json:"owners,omitempty"`
	Error   string   `json:"error,omitempty"`
	Code    string   `json:"code,omitempty"`
}

type batchDeleteResponse struct {
	Results []batchDeleteResultWire `json:"results"`
	Node    string                  `json:"node"`
}

// --- commands ---

// BatchSet stores multiple key/value pairs in a single round-trip.
// Each item's TTL is honored independently (TTL <= 0 = no expiry).
//
// The returned slice mirrors items in order. Per-item failures
// (cluster draining, oversize value, etc.) surface via the result's
// Err field; the method itself returns nil error as long as the
// HTTP call succeeded. Empty input is a no-op that returns an
// empty slice and nil error.
func (c *Client) BatchSet(ctx context.Context, items []BatchSetItem) ([]BatchPutResult, error) {
	if len(items) == 0 {
		return []BatchPutResult{}, nil
	}

	body := batchPutRequest{Items: make([]batchPutItemWire, 0, len(items))}
	for _, it := range items {
		wire := batchPutItemWire{
			Key:           it.Key,
			Value:         base64.StdEncoding.EncodeToString(it.Value),
			ValueEncoding: "base64",
		}
		if it.TTL > 0 {
			wire.TTLMs = it.TTL.Milliseconds()
		}

		body.Items = append(body.Items, wire)
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal batch set: %w", err)
	}

	resp, err := c.do(ctx, http.MethodPost, "/v1/cache/batch/put", bytes.NewReader(encoded), map[string]string{
		"Content-Type": contentTypeJSON,
	})
	if err != nil {
		return nil, err
	}

	defer closeBody(resp)

	if resp.StatusCode != http.StatusOK {
		return nil, classifyResponse(resp)
	}

	var out batchPutResponse

	decodeErr := json.NewDecoder(resp.Body).Decode(&out)
	if decodeErr != nil {
		return nil, fmt.Errorf("decode batch set response: %w", decodeErr)
	}

	results := make([]BatchPutResult, 0, len(out.Results))
	for _, r := range out.Results {
		results = append(results, BatchPutResult{
			Key:    r.Key,
			Stored: r.Stored,
			Bytes:  r.Bytes,
			Owners: r.Owners,
			Err:    statusErrorFromBatch(r.Stored, r.Code, r.Error),
		})
	}

	return results, nil
}

// BatchGet fetches multiple keys in a single round-trip. Each
// result carries a Found flag — true means the key was present
// and Item is populated; false means the key was missing and Item
// is nil. Missing keys are NOT errors at the call level; the HTTP
// call succeeds and the per-key Found flag does the discrimination.
//
// Empty input is a no-op that returns an empty slice and nil error.
func (c *Client) BatchGet(ctx context.Context, keys []string) ([]BatchGetResult, error) {
	if len(keys) == 0 {
		return []BatchGetResult{}, nil
	}

	body := batchGetRequest{Keys: keys}

	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal batch get: %w", err)
	}

	resp, err := c.do(ctx, http.MethodPost, "/v1/cache/batch/get", bytes.NewReader(encoded), map[string]string{
		"Content-Type": contentTypeJSON,
	})
	if err != nil {
		return nil, err
	}

	defer closeBody(resp)

	if resp.StatusCode != http.StatusOK {
		return nil, classifyResponse(resp)
	}

	var out batchGetResponse

	decodeErr := json.NewDecoder(resp.Body).Decode(&out)
	if decodeErr != nil {
		return nil, fmt.Errorf("decode batch get response: %w", decodeErr)
	}

	results := make([]BatchGetResult, 0, len(out.Results))
	for _, r := range out.Results {
		result := BatchGetResult{Key: r.Key, Found: r.Found}
		if r.Found {
			item, itemErr := itemFromBatchGet(r, out.Node)
			if itemErr != nil {
				return nil, fmt.Errorf("decode batch get item %q: %w", r.Key, itemErr)
			}

			result.Item = item
		}

		results = append(results, result)
	}

	return results, nil
}

// BatchDelete removes multiple keys in a single round-trip. Like
// the single-key Delete, the operation is idempotent — deleting
// missing keys is reported as Deleted=true. Err is non-nil only
// when the cluster could not service the delete (draining,
// internal error).
//
// Empty input is a no-op that returns an empty slice and nil error.
func (c *Client) BatchDelete(ctx context.Context, keys []string) ([]BatchDeleteResult, error) {
	if len(keys) == 0 {
		return []BatchDeleteResult{}, nil
	}

	body := batchDeleteRequest{Keys: keys}

	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal batch delete: %w", err)
	}

	resp, err := c.do(ctx, http.MethodPost, "/v1/cache/batch/delete", bytes.NewReader(encoded), map[string]string{
		"Content-Type": contentTypeJSON,
	})
	if err != nil {
		return nil, err
	}

	defer closeBody(resp)

	if resp.StatusCode != http.StatusOK {
		return nil, classifyResponse(resp)
	}

	var out batchDeleteResponse

	decodeErr := json.NewDecoder(resp.Body).Decode(&out)
	if decodeErr != nil {
		return nil, fmt.Errorf("decode batch delete response: %w", decodeErr)
	}

	results := make([]BatchDeleteResult, 0, len(out.Results))
	for _, r := range out.Results {
		results = append(results, BatchDeleteResult{
			Key:     r.Key,
			Deleted: r.Deleted,
			Owners:  r.Owners,
			Err:     statusErrorFromBatch(r.Deleted, r.Code, r.Error),
		})
	}

	return results, nil
}

// --- helpers ---

// statusErrorFromBatch builds a *StatusError from a per-item batch
// result's Code/Error fields. Returns nil when the per-item
// operation succeeded — `success && code == ""` is the all-clear
// shape. Callers carry the result back to user code unchanged.
func statusErrorFromBatch(success bool, code, message string) *StatusError {
	if success && code == "" {
		return nil
	}

	if code == "" && message == "" {
		return nil
	}

	// Map the canonical batch Code values back to HTTP status so the
	// resulting *StatusError honors errors.Is the same way a top-
	// level call's StatusError does. The batch wire only carries
	// BAD_REQUEST / DRAINING / INTERNAL today; future codes get
	// passed through with HTTPStatus=0 and rely on Code alone.
	var status int

	switch code {
	case "BAD_REQUEST":
		status = http.StatusBadRequest
	case "DRAINING":
		status = http.StatusServiceUnavailable
	case "INTERNAL":
		status = http.StatusInternalServerError
	default:
		// Unknown code from the wire: leave HTTPStatus zero and let
		// callers match on Code alone (or string-compare Message for
		// unstable failure modes the codebook doesn't cover).
		status = 0
	}

	return &StatusError{
		HTTPStatus: status,
		Code:       code,
		Message:    message,
	}
}

// itemFromBatchGet lifts a batchGetResultWire into the public Item
// shape, decoding the base64 value and threading the response-level
// node ID through. Reuses decodeEnvelopeValue from the single-key
// path for consistency.
func itemFromBatchGet(r batchGetResultWire, node string) (*Item, error) {
	value, err := decodeEnvelopeValue(itemEnvelope{
		Value:         r.Value,
		ValueEncoding: r.ValueEncoding,
	})
	if err != nil {
		return nil, err
	}

	return &Item{
		Key:         r.Key,
		Value:       value,
		TTLMs:       r.TTLMs,
		ExpiresAt:   r.ExpiresAt,
		Version:     r.Version,
		Origin:      r.Origin,
		LastUpdated: r.LastUpdated,
		Node:        node,
		Owners:      r.Owners,
	}, nil
}
