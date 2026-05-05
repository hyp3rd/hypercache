package backend

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
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
	// auth (zero-value = disabled) decorates outgoing requests with a
	// bearer token or custom signing function. Server-side validation
	// lives on distHTTPServer; the two share the same DistHTTPAuth
	// struct when constructed via NewDistHTTPTransportWithAuth.
	auth DistHTTPAuth
	// compressionThreshold is the body-size byte threshold above
	// which Set request bodies are gzip-compressed. <=0 disables —
	// matches the pre-Phase-B wire format byte-for-byte.
	compressionThreshold int
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
	return NewDistHTTPTransportWithAuth(limits, DistHTTPAuth{}, resolver)
}

// NewDistHTTPTransportWithAuth combines explicit limits and auth policy in
// a single constructor. DistMemory uses this when WithDistHTTPAuth is set
// so the auto-created HTTP client signs requests with the same token the
// server validates against.
//
// If limits.TLSConfig is non-nil, the underlying http.Transport is
// configured with the same *tls.Config used by the server, so client
// connections to peer https:// endpoints handshake against the same
// roots and certificates.
func NewDistHTTPTransportWithAuth(limits DistHTTPLimits, auth DistHTTPAuth, resolver func(string) (string, bool)) *DistHTTPTransport {
	limits = limits.withDefaults()

	client := &http.Client{Timeout: limits.ClientTimeout}
	if limits.TLSConfig != nil {
		// Clone http.DefaultTransport's settings (timeouts, idle pools)
		// then attach the TLS config. Cloning vs constructing from
		// scratch avoids reinventing default timeouts that future Go
		// versions may tighten.
		tr, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			// Defensive: stdlib has always returned *http.Transport
			// here, but if a third party rewrote DefaultTransport at
			// init we still want a usable client.
			tr = &http.Transport{}
		} else {
			tr = tr.Clone()
		}

		// Clone the TLS config and force HTTP/1.1 via ALPN. The dist
		// HTTP server is fiber+fasthttp which speaks HTTP/1.1 only —
		// without this constraint Go's stdlib transport advertises h2
		// via ALPN, succeeds the handshake, then immediately fails
		// reading the response with "http2: frame too large, note that
		// the frame header looked like an HTTP/1.1 header". The
		// http/1.1 NextProto override is the canonical fix from
		// net/http docs.
		tlsConf := limits.TLSConfig.Clone()
		if len(tlsConf.NextProtos) == 0 {
			tlsConf.NextProtos = []string{"http/1.1"}
		}

		tr.TLSClientConfig = tlsConf
		tr.ForceAttemptHTTP2 = false
		tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
		client.Transport = tr
	}

	return &DistHTTPTransport{
		client:               client,
		baseURLFn:            resolver,
		respBodyLimit:        limits.ResponseLimit,
		auth:                 auth,
		compressionThreshold: limits.CompressionThreshold,
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

	reqBodyReader, gzipped, err := t.maybeGzip(payloadBytes)
	if err != nil {
		return ewrap.Wrap(err, "gzip set body")
	}

	// prefer canonical endpoint; legacy /internal/cache/set still served
	hreq, err := t.newNodeRequest(ctx, http.MethodPost, nodeID, "/internal/set", nil, reqBodyReader)
	if err != nil {
		return ewrap.Wrap(err, errMsgNewRequest)
	}

	hreq.Header.Set("Content-Type", "application/json")

	if gzipped {
		hreq.Header.Set("Content-Encoding", "gzip")
	}

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

// IndirectHealth asks the relay node to probe the target on this
// caller's behalf. The dist HTTP server's `/internal/probe?target=<id>`
// endpoint runs a Health() call on its own transport and returns 200 if
// the target is reachable from the relay's vantage point. Used by the
// SWIM indirect-probe path to filter caller-side network blips before
// marking a peer suspect.
func (t *DistHTTPTransport) IndirectHealth(ctx context.Context, relayNodeID, targetNodeID string) error {
	hreq, err := t.newNodeRequest(ctx, http.MethodGet, relayNodeID, "/internal/probe",
		url.Values{"target": []string{targetNodeID}}, nil)
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
		return ewrap.Newf("indirect probe status %d", resp.StatusCode)
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

// ListKeys returns all keys from a remote node (expensive; used for
// tests / anti-entropy fallback). Walks the cursor-paginated
// `/internal/keys` endpoint introduced in Phase C.2 — callers
// continue to receive the full key set unchanged.
func (t *DistHTTPTransport) ListKeys(ctx context.Context, nodeID string) ([]string, error) {
	var (
		all    []string
		cursor string
	)

	const safetyLimit = 1024 // upper bound to prevent infinite loop on a buggy server

	for range safetyLimit {
		page, err := t.listKeysPage(ctx, nodeID, cursor)
		if err != nil {
			return nil, err
		}

		all = append(all, page.Keys...)

		if page.NextCursor == "" {
			return all, nil
		}

		// Truncated pages return next_cursor == current cursor.
		// Without bumping limit, we'd loop forever — but ListKeys
		// never sets a limit (it asks for the full shard each time),
		// so server-side truncation cannot occur on this path.
		// Defensive: break if cursor doesn't advance.
		if page.NextCursor == cursor {
			break
		}

		cursor = page.NextCursor
	}

	return all, nil
}

// listKeysPage is the per-page fetch for ListKeys; broken out so the
// pagination loop above stays readable.
func (t *DistHTTPTransport) listKeysPage(ctx context.Context, nodeID, cursor string) (keysPageResp, error) {
	var query url.Values

	if cursor != "" {
		query = url.Values{"cursor": []string{cursor}}
	}

	hreq, err := t.newNodeRequest(ctx, http.MethodGet, nodeID, "/internal/keys", query, nil)
	if err != nil {
		return keysPageResp{}, ewrap.Wrap(err, errMsgNewRequest)
	}

	resp, err := t.doTrusted(hreq)
	if err != nil {
		return keysPageResp{}, err
	}

	respBody := t.limitedBody(resp)
	defer drainBody(respBody)

	if resp.StatusCode >= statusThreshold {
		return keysPageResp{}, ewrap.Newf("list keys status %d", resp.StatusCode)
	}

	var page keysPageResp

	unmarshalErr := readAndUnmarshal(respBody, &page)
	if unmarshalErr != nil {
		return keysPageResp{}, unmarshalErr
	}

	return page, nil
}

// keysPageResp matches the JSON shape returned by /internal/keys —
// kept private to the transport since handleKeys is the source of
// truth for the wire format.
type keysPageResp struct {
	Keys       []string `json:"keys"`
	NextCursor string   `json:"next_cursor"`
	Truncated  bool     `json:"truncated"`
}

// limitedBody wraps resp.Body so reads beyond respBodyLimit return
// *http.MaxBytesError. Returns the original body when the limit is <=0.
func (t *DistHTTPTransport) limitedBody(resp *http.Response) io.ReadCloser {
	if t.respBodyLimit <= 0 {
		return resp.Body
	}

	return http.MaxBytesReader(nil, resp.Body, t.respBodyLimit)
}

// maybeGzip returns a reader for the request body and a boolean
// indicating whether the body was gzip-compressed. Compression
// applies when compressionThreshold > 0 and the payload exceeds it.
// Below the threshold the original bytes round-trip unchanged so
// peers without compression support remain compatible. Errors come
// only from the gzip writer (which closes around an in-memory
// buffer, so they are practically impossible) — propagated for
// completeness.
func (t *DistHTTPTransport) maybeGzip(payload []byte) (io.Reader, bool, error) {
	if t.compressionThreshold <= 0 || len(payload) <= t.compressionThreshold {
		return bytes.NewReader(payload), false, nil
	}

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)

	_, writeErr := gz.Write(payload)
	if writeErr != nil {
		return nil, false, ewrap.Wrap(writeErr, "gzip write")
	}

	closeErr := gz.Close()
	if closeErr != nil {
		return nil, false, ewrap.Wrap(closeErr, "gzip close")
	}

	return &buf, true, nil
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

	err = t.auth.sign(req)
	if err != nil {
		return nil, ewrap.Wrap(err, "sign request")
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
