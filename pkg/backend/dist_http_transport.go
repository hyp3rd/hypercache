// Package backend provides backend implementations including a distributed HTTP transport.
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
type DistHTTPTransport struct { // minimal MVP
	client    *http.Client
	baseURLFn func(nodeID string) (string, bool) // resolves nodeID -> base URL (scheme+host)
}

// internal status code threshold for error classification.
const statusThreshold = 300

// NewDistHTTPTransport creates a new HTTP transport.
func NewDistHTTPTransport(timeout time.Duration, resolver func(string) (string, bool)) *DistHTTPTransport {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}

	return &DistHTTPTransport{
		client:    &http.Client{Timeout: timeout},
		baseURLFn: resolver,
	}
}

// request/response DTOs moved to dist_http_types.go

const (
	errMsgNewRequest = "new request"
	errMsgDoRequest  = "do request"
)

// ForwardSet forwards a set (or replication) request to a remote node.
func (t *DistHTTPTransport) ForwardSet(ctx context.Context, nodeID string, item *cache.Item, replicate bool) error { //nolint:ireturn
	base, ok := t.baseURLFn(nodeID)
	if !ok {
		return sentinel.ErrBackendNotFound
	}

	reqBody := httpSetRequest{ // split for line length
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

	url := base + "/internal/cache/set"

	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payloadBytes))
	if err != nil {
		return ewrap.Wrap(err, errMsgNewRequest)
	}

	hreq.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(hreq)
	if err != nil {
		return ewrap.Wrap(err, errMsgDoRequest)
	}

	defer func() {
		_ = resp.Body.Close() //nolint:errcheck // best-effort
	}()

	if resp.StatusCode == http.StatusNotFound {
		return sentinel.ErrBackendNotFound
	}

	const statusThreshold = 300 // local redeclare for linter clarity
	if resp.StatusCode >= statusThreshold {
		body, rerr := io.ReadAll(resp.Body)
		if rerr != nil {
			return ewrap.Wrap(rerr, "read error body")
		}

		return ewrap.Newf("forward set status %d body %s", resp.StatusCode, string(body))
	}

	return nil
}

// type httpGetResponse formerly used for direct decoding; replaced by map-based decoding to satisfy linters.

// ForwardGet fetches an item from a remote node.
func (t *DistHTTPTransport) ForwardGet(ctx context.Context, nodeID string, key string) (*cache.Item, bool, error) { //nolint:ireturn
	base, ok := t.baseURLFn(nodeID)
	if !ok {
		return nil, false, sentinel.ErrBackendNotFound
	}

	url := fmt.Sprintf("%s/internal/cache/get?key=%s", base, key)

	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, ewrap.Wrap(err, errMsgNewRequest)
	}

	resp, err := t.client.Do(hreq)
	if err != nil {
		return nil, false, ewrap.Wrap(err, errMsgDoRequest)
	}

	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // best-effort

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

// decodeGetBody decodes a get response body into an item.
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
		// define minimal mirror struct to ensure json tags present (satisfy musttag)
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
		// reconstruct cache.Item (we ignore expiration formatting difference vs ms)
		return &cache.Item{ // multi-line for readability
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

// ForwardRemove forwards a remove operation.
func (t *DistHTTPTransport) ForwardRemove(ctx context.Context, nodeID string, key string, replicate bool) error { //nolint:ireturn
	base, ok := t.baseURLFn(nodeID)
	if !ok {
		return sentinel.ErrBackendNotFound
	}

	url := fmt.Sprintf("%s/internal/cache/remove?key=%s&replicate=%t", base, key, replicate)

	hreq, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return ewrap.Wrap(err, errMsgNewRequest)
	}

	resp, err := t.client.Do(hreq)
	if err != nil {
		return ewrap.Wrap(err, errMsgDoRequest)
	}

	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // best-effort

	if resp.StatusCode == http.StatusNotFound {
		return sentinel.ErrBackendNotFound
	}

	if resp.StatusCode >= statusThreshold {
		return ewrap.Newf("forward remove status %d", resp.StatusCode)
	}

	return nil
}

// Health probes a remote node health endpoint.
func (t *DistHTTPTransport) Health(ctx context.Context, nodeID string) error { //nolint:ireturn
	base, ok := t.baseURLFn(nodeID)
	if !ok {
		return sentinel.ErrBackendNotFound
	}

	url := base + "/health"

	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ewrap.Wrap(err, errMsgNewRequest)
	}

	resp, err := t.client.Do(hreq)
	if err != nil {
		return ewrap.Wrap(err, errMsgDoRequest)
	}

	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // best-effort

	if resp.StatusCode == http.StatusNotFound {
		return sentinel.ErrBackendNotFound
	}

	if resp.StatusCode >= statusThreshold {
		return ewrap.Newf("health status %d", resp.StatusCode)
	}

	return nil
}
