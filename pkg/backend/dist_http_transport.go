package backend

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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
	// respBodyLimit caps response bodies so a malicious or compromised
	// peer cannot OOM the requester via a giant response. <=0 disables.
	respBodyLimit int64
}

const statusThreshold = 300

// NewDistHTTPTransport constructs a DistHTTPTransport with the given timeout and
// nodeID->baseURL resolver. Timeout <=0 defaults to defaultDistHTTPClientTimeout.
// Response bodies are bounded by defaultDistHTTPResponseLimit; use
// NewDistHTTPTransportWithLimits to override.
func NewDistHTTPTransport(timeout time.Duration, resolver func(string) (string, bool)) *DistHTTPTransport {
	if timeout <= 0 {
		timeout = defaultDistHTTPClientTimeout
	}

	return &DistHTTPTransport{
		client:        &http.Client{Timeout: timeout},
		baseURLFn:     resolver,
		respBodyLimit: defaultDistHTTPResponseLimit,
	}
}

// NewDistHTTPTransportWithLimits is the explicit-limits variant. Use it when
// the caller needs to raise/lower the response-body cap or align the client
// timeout with custom DistHTTPLimits applied to the server.
func NewDistHTTPTransportWithLimits(limits DistHTTPLimits, resolver func(string) (string, bool)) *DistHTTPTransport {
	limits = limits.withDefaults()

	return &DistHTTPTransport{
		client:        &http.Client{Timeout: limits.ClientTimeout},
		baseURLFn:     resolver,
		respBodyLimit: limits.ResponseLimit,
	}
}

// drainBody consumes any remaining bytes (so the connection can be reused
// from the keep-alive pool) and closes the body. Centralizes the drain+close
// pattern so each call site stays a one-line defer. Errors are intentionally
// dropped — the connection is being recycled either way.
func drainBody(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, body)
	_ = body.Close()
}

// readAndUnmarshal reads the entire bounded body into memory, then
// json.Unmarshals it into dst. We pre-read so that an *http.MaxBytesError
// from the limited body surfaces cleanly to the caller — the goccy/go-json
// streaming decoder swallows it as io.EOF / "unexpected end of JSON input",
// which would mask the real "peer sent too much" failure mode behind a
// generic parse error. Read cost is bounded by respBodyLimit.
func readAndUnmarshal(body io.Reader, dst any) error {
	buf, err := io.ReadAll(body)
	if err != nil {
		return ewrap.Wrap(err, "read response body")
	}

	err = json.Unmarshal(buf, dst)
	if err != nil {
		return ewrap.Wrap(err, "unmarshal response body")
	}

	return nil
}

const (
	errMsgNewRequest = "new request"
	errMsgDoRequest  = "do request"
)

// ForwardSet sends a Set/Replicate request to a remote node.
func (t *DistHTTPTransport) ForwardSet(ctx context.Context, nodeID string, item *cache.Item, replicate bool) error {
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
	hreq, err := t.newNodeRequest(ctx, http.MethodPost, nodeID, "/internal/set", nil, bytes.NewReader(payloadBytes))
	if err != nil {
		return ewrap.Wrap(err, errMsgNewRequest)
	}

	hreq.Header.Set("Content-Type", "application/json")

	resp, err := t.doTrusted(hreq)
	if err != nil {
		return err
	}

	body := t.limitedBody(resp)
	defer drainBody(body)

	if resp.StatusCode == http.StatusNotFound {
		return sentinel.ErrBackendNotFound
	}

	if resp.StatusCode >= statusThreshold {
		errBody, rerr := io.ReadAll(body)
		if rerr != nil {
			return ewrap.Wrap(rerr, "read error body")
		}

		return ewrap.Newf("forward set status %d body %s", resp.StatusCode, string(errBody))
	}

	return nil
}

// ForwardGet fetches a single item from a remote node.
func (t *DistHTTPTransport) ForwardGet(ctx context.Context, nodeID, key string) (*cache.Item, bool, error) {
	// prefer canonical endpoint
	hreq, err := t.newNodeRequest(ctx, http.MethodGet, nodeID, "/internal/get", url.Values{
		"key": {key},
	}, nil)
	if err != nil {
		return nil, false, ewrap.Wrap(err, errMsgNewRequest)
	}

	resp, err := t.doTrusted(hreq)
	if err != nil {
		return nil, false, err
	}

	body := t.limitedBody(resp)
	defer drainBody(body)

	if resp.StatusCode == http.StatusNotFound {
		return nil, false, sentinel.ErrBackendNotFound
	}

	if resp.StatusCode != http.StatusOK {
		return nil, false, ewrap.Newf("forward get status %d", resp.StatusCode)
	}

	item, found, derr := decodeGetBody(body)
	if derr != nil {
		return nil, false, derr
	}

	if !found {
		return nil, false, nil
	}

	return item, true, nil
}

func decodeGetBody(r io.Reader) (*cache.Item, bool, error) {
	var raw map[string]json.RawMessage

	err := readAndUnmarshal(r, &raw)
	if err != nil {
		return nil, false, err
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
func (t *DistHTTPTransport) ForwardRemove(ctx context.Context, nodeID, key string, replicate bool) error {
	// prefer canonical endpoint
	hreq, err := t.newNodeRequest(ctx, http.MethodDelete, nodeID, "/internal/del", url.Values{
		"key":       {key},
		"replicate": {strconv.FormatBool(replicate)},
	}, nil)
	if err != nil {
		return ewrap.Wrap(err, errMsgNewRequest)
	}

	resp, err := t.doTrusted(hreq)
	if err != nil {
		return err
	}

	defer drainBody(t.limitedBody(resp))

	if resp.StatusCode == http.StatusNotFound {
		return sentinel.ErrBackendNotFound
	}

	if resp.StatusCode >= statusThreshold {
		return ewrap.Newf("forward remove status %d", resp.StatusCode)
	}

	return nil
}

// Health performs a health probe against a remote node.
func (t *DistHTTPTransport) Health(ctx context.Context, nodeID string) error {
	hreq, err := t.newNodeRequest(ctx, http.MethodGet, nodeID, "/health", nil, nil)
	if err != nil {
		return ewrap.Wrap(err, errMsgNewRequest)
	}

	resp, err := t.doTrusted(hreq)
	if err != nil {
		return err
	}

	defer drainBody(t.limitedBody(resp))

	if resp.StatusCode == http.StatusNotFound {
		return sentinel.ErrBackendNotFound
	}

	if resp.StatusCode >= statusThreshold {
		return ewrap.Newf("health status %d", resp.StatusCode)
	}

	return nil
}

// FetchMerkle retrieves a Merkle tree snapshot from a remote node.
func (t *DistHTTPTransport) FetchMerkle(ctx context.Context, nodeID string) (*MerkleTree, error) {
	if t == nil {
		return nil, errNoTransport
	}

	hreq, err := t.newNodeRequest(ctx, http.MethodGet, nodeID, "/internal/merkle", nil, nil)
	if err != nil {
		return nil, ewrap.Wrap(err, errMsgNewRequest)
	}

	resp, err := t.doTrusted(hreq)
	if err != nil {
		return nil, err
	}

	respBody := t.limitedBody(resp)
	defer drainBody(respBody)

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

	err = readAndUnmarshal(respBody, &body)
	if err != nil {
		return nil, err
	}

	return &MerkleTree{Root: body.Root, LeafHashes: body.LeafHashes, ChunkSize: body.ChunkSize}, nil
}

// ListKeys returns all keys from a remote node (expensive; used for tests / anti-entropy fallback).
func (t *DistHTTPTransport) ListKeys(ctx context.Context, nodeID string) ([]string, error) {
	hreq, err := t.newNodeRequest(ctx, http.MethodGet, nodeID, "/internal/keys", nil, nil)
	if err != nil {
		return nil, ewrap.Wrap(err, errMsgNewRequest)
	}

	resp, err := t.doTrusted(hreq)
	if err != nil {
		return nil, err
	}

	respBody := t.limitedBody(resp)
	defer drainBody(respBody)

	if resp.StatusCode >= statusThreshold {
		return nil, ewrap.Newf("list keys status %d", resp.StatusCode)
	}

	var body struct {
		Keys []string `json:"keys"`
	}

	err = readAndUnmarshal(respBody, &body)
	if err != nil {
		return nil, err
	}

	return body.Keys, nil
}

// limitedBody wraps resp.Body so reads beyond respBodyLimit return
// *http.MaxBytesError. Returns the original body when the limit is <=0.
func (t *DistHTTPTransport) limitedBody(resp *http.Response) io.ReadCloser {
	if t.respBodyLimit <= 0 {
		return resp.Body
	}

	return http.MaxBytesReader(nil, resp.Body, t.respBodyLimit)
}

func (t *DistHTTPTransport) resolveBaseURL(nodeID string) (*url.URL, error) {
	if t == nil || t.baseURLFn == nil {
		return nil, errNoTransport
	}

	base, ok := t.baseURLFn(nodeID)
	if !ok {
		return nil, sentinel.ErrBackendNotFound
	}

	parsed, err := url.Parse(base)
	if err != nil {
		return nil, ewrap.Wrap(err, "parse base url")
	}

	if !parsed.IsAbs() || parsed.Host == "" {
		return nil, ewrap.Newf("invalid base url for node %q", nodeID)
	}

	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return nil, ewrap.Newf("unsupported base url scheme %q", parsed.Scheme)
	}

	return parsed, nil
}

func (t *DistHTTPTransport) newNodeRequest(
	ctx context.Context,
	method, nodeID, endpointPath string,
	query url.Values,
	body io.Reader,
) (*http.Request, error) {
	base, err := t.resolveBaseURL(nodeID)
	if err != nil {
		return nil, err
	}

	target, err := url.JoinPath(base.String(), strings.TrimPrefix(endpointPath, "/"))
	if err != nil {
		return nil, ewrap.Wrap(err, "join base url path")
	}

	targetURL, err := url.Parse(target)
	if err != nil {
		return nil, ewrap.Wrap(err, "parse target url")
	}

	if len(query) > 0 {
		targetURL.RawQuery = query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, targetURL.String(), body)
	if err != nil {
		return nil, ewrap.Wrap(err, "create new request")
	}

	return req, nil
}

func (t *DistHTTPTransport) doTrusted(hreq *http.Request) (*http.Response, error) {
	// URL is built from the internal cluster resolver and scheme/host validated in newNodeRequest.
	resp, err := t.client.Do(hreq) // #nosec G704 -- trusted cluster node URL, not user-supplied arbitrary endpoint
	if err != nil {
		return nil, ewrap.Wrap(err, errMsgDoRequest)
	}

	return resp, nil
}
