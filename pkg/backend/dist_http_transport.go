package backend

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/goccy/go-json"

	"github.com/hyp3rd/ewrap"

	"github.com/hyp3rd/hypercache/internal/sentinel"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// DistHTTPTransport implements DistTransport over HTTP JSON.
type DistHTTPTransport struct {
	client    *http.Client
	baseURLFn func(nodeID string) (string, bool)
}

const statusThreshold = 300

// NewDistHTTPTransport constructs a DistHTTPTransport with the given timeout and
// nodeID->baseURL resolver. Timeout <=0 defaults to 2s.
func NewDistHTTPTransport(timeout time.Duration, resolver func(string) (string, bool)) *DistHTTPTransport { //nolint:ireturn
	if timeout <= 0 {
		timeout = 2 * time.Second
	}

	return &DistHTTPTransport{client: &http.Client{Timeout: timeout}, baseURLFn: resolver}
}

const (
	errMsgNewRequest = "new request"
	errMsgDoRequest  = "do request"
)

// ForwardSet sends a Set/Replicate request to a remote node.
func (t *DistHTTPTransport) ForwardSet(ctx context.Context, nodeID string, item *cache.Item, replicate bool) error { //nolint:ireturn
	base, ok := t.baseURLFn(nodeID)
	if !ok {
		return sentinel.ErrBackendNotFound
	}

	reqBody := httpSetRequest{
		Key:        item.Key,
		Value:      item.Value,
		Expiration: item.Expiration.Milliseconds(),
		Version:    item.Version,
		Origin:     item.Origin,
		Replicate:  replicate,
	}

	payloadBytes, err := json.Marshal(&reqBody)
	if err != nil {
		return ewrap.Wrap(err, "marshal set request")
	}

	// prefer canonical endpoint; legacy /internal/cache/set still served
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/internal/set", bytes.NewReader(payloadBytes))
	if err != nil {
		return ewrap.Wrap(err, errMsgNewRequest)
	}

	hreq.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(hreq)
	if err != nil {
		return ewrap.Wrap(err, errMsgDoRequest)
	}

	defer func() { _ = resp.Body.Close() }() //nolint:errcheck

	if resp.StatusCode == http.StatusNotFound {
		return sentinel.ErrBackendNotFound
	}

	if resp.StatusCode >= statusThreshold {
		body, rerr := io.ReadAll(resp.Body)
		if rerr != nil {
			return ewrap.Wrap(rerr, "read error body")
		}

		return ewrap.Newf("forward set status %d body %s", resp.StatusCode, string(body))
	}

	return nil
}

// ForwardGet fetches a single item from a remote node.
func (t *DistHTTPTransport) ForwardGet(ctx context.Context, nodeID, key string) (*cache.Item, bool, error) { //nolint:ireturn
	base, ok := t.baseURLFn(nodeID)
	if !ok {
		return nil, false, sentinel.ErrBackendNotFound
	}

	// prefer canonical endpoint
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/internal/get?key=%s", base, key), nil)
	if err != nil {
		return nil, false, ewrap.Wrap(err, errMsgNewRequest)
	}

	resp, err := t.client.Do(hreq)
	if err != nil {
		return nil, false, ewrap.Wrap(err, errMsgDoRequest)
	}

	defer func() { _ = resp.Body.Close() }() //nolint:errcheck

	if resp.StatusCode == http.StatusNotFound {
		return nil, false, sentinel.ErrBackendNotFound
	}

	if resp.StatusCode != http.StatusOK {
		return nil, false, ewrap.Newf("forward get status %d", resp.StatusCode)
	}

	item, found, derr := decodeGetBody(resp.Body)
	if derr != nil {
		return nil, false, derr
	}

	if !found {
		return nil, false, nil
	}

	return item, true, nil
}

func decodeGetBody(r io.Reader) (*cache.Item, bool, error) { //nolint:ireturn
	var raw map[string]json.RawMessage

	dec := json.NewDecoder(r)

	err := dec.Decode(&raw)
	if err != nil {
		return nil, false, ewrap.Wrap(err, "decode body")
	}

	var found bool
	if fb, ok := raw["found"]; ok {
		err := json.Unmarshal(fb, &found)
		if err != nil {
			return nil, false, ewrap.Wrap(err, "unmarshal found")
		}
	}

	if !found {
		return nil, false, nil
	}

	if ib, ok := raw["item"]; ok && len(ib) > 0 {
		var mirror struct {
			Key        string          `json:"key"`
			Value      json.RawMessage `json:"value"`
			Expiration int64           `json:"expiration"`
			Version    uint64          `json:"version"`
			Origin     string          `json:"origin"`
		}

		err := json.Unmarshal(ib, &mirror)
		if err != nil {
			return nil, false, ewrap.Wrap(err, "unmarshal mirror")
		}

		return &cache.Item{
			Key:         mirror.Key,
			Value:       mirror.Value,
			Expiration:  time.Duration(mirror.Expiration) * time.Millisecond,
			Version:     mirror.Version,
			Origin:      mirror.Origin,
			LastUpdated: time.Now(),
		}, true, nil
	}

	return &cache.Item{}, true, nil
}

// ForwardRemove propagates a delete operation to a remote node.
func (t *DistHTTPTransport) ForwardRemove(ctx context.Context, nodeID, key string, replicate bool) error { //nolint:ireturn
	base, ok := t.baseURLFn(nodeID)
	if !ok {
		return sentinel.ErrBackendNotFound
	}

	// prefer canonical endpoint
	hreq, err := http.NewRequestWithContext(ctx, http.MethodDelete, fmt.Sprintf("%s/internal/del?key=%s&replicate=%t", base, key, replicate), nil)
	if err != nil {
		return ewrap.Wrap(err, errMsgNewRequest)
	}

	resp, err := t.client.Do(hreq)
	if err != nil {
		return ewrap.Wrap(err, errMsgDoRequest)
	}

	defer func() { _ = resp.Body.Close() }() //nolint:errcheck

	if resp.StatusCode == http.StatusNotFound {
		return sentinel.ErrBackendNotFound
	}

	if resp.StatusCode >= statusThreshold {
		return ewrap.Newf("forward remove status %d", resp.StatusCode)
	}

	return nil
}

// Health performs a health probe against a remote node.
func (t *DistHTTPTransport) Health(ctx context.Context, nodeID string) error { //nolint:ireturn
	base, ok := t.baseURLFn(nodeID)
	if !ok {
		return sentinel.ErrBackendNotFound
	}

	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
	if err != nil {
		return ewrap.Wrap(err, errMsgNewRequest)
	}

	resp, err := t.client.Do(hreq)
	if err != nil {
		return ewrap.Wrap(err, errMsgDoRequest)
	}

	defer func() { _ = resp.Body.Close() }() //nolint:errcheck

	if resp.StatusCode == http.StatusNotFound {
		return sentinel.ErrBackendNotFound
	}

	if resp.StatusCode >= statusThreshold {
		return ewrap.Newf("health status %d", resp.StatusCode)
	}

	return nil
}

// FetchMerkle retrieves a Merkle tree snapshot from a remote node.
func (t *DistHTTPTransport) FetchMerkle(ctx context.Context, nodeID string) (*MerkleTree, error) { //nolint:ireturn
	if t == nil {
		return nil, errNoTransport
	}

	base, ok := t.baseURLFn(nodeID)
	if !ok {
		return nil, sentinel.ErrBackendNotFound
	}

	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/internal/merkle", nil)
	if err != nil {
		return nil, ewrap.Wrap(err, errMsgNewRequest)
	}

	resp, err := t.client.Do(hreq)
	if err != nil {
		return nil, ewrap.Wrap(err, errMsgDoRequest)
	}

	defer func() { _ = resp.Body.Close() }() //nolint:errcheck

	if resp.StatusCode == http.StatusNotFound {
		return nil, sentinel.ErrBackendNotFound
	}

	if resp.StatusCode >= statusThreshold {
		return nil, ewrap.Newf("fetch merkle status %d", resp.StatusCode)
	}

	var body struct {
		Root       []byte   `json:"root"`
		LeafHashes [][]byte `json:"leaf_hashes"`
		ChunkSize  int      `json:"chunk_size"`
	}

	dec := json.NewDecoder(resp.Body)

	err = dec.Decode(&body)
	if err != nil {
		return nil, ewrap.Wrap(err, "decode merkle")
	}

	return &MerkleTree{Root: body.Root, LeafHashes: body.LeafHashes, ChunkSize: body.ChunkSize}, nil
}

// ListKeys returns all keys from a remote node (expensive; used for tests / anti-entropy fallback).
func (t *DistHTTPTransport) ListKeys(ctx context.Context, nodeID string) ([]string, error) { //nolint:ireturn
	base, ok := t.baseURLFn(nodeID)
	if !ok {
		return nil, sentinel.ErrBackendNotFound
	}

	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/internal/keys", nil)
	if err != nil {
		return nil, ewrap.Wrap(err, errMsgNewRequest)
	}

	resp, err := t.client.Do(hreq)
	if err != nil {
		return nil, ewrap.Wrap(err, errMsgDoRequest)
	}

	defer func() { _ = resp.Body.Close() }() //nolint:errcheck

	if resp.StatusCode >= statusThreshold {
		return nil, ewrap.Newf("list keys status %d", resp.StatusCode)
	}

	var body struct {
		Keys []string `json:"keys"`
	}

	dec := json.NewDecoder(resp.Body)

	err = dec.Decode(&body)
	if err != nil {
		return nil, ewrap.Wrap(err, "decode keys")
	}

	return body.Keys, nil
}
