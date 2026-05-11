package client

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	mathrand "math/rand/v2"
	"net/http"
	"sync"
)

// httpErrorStatusFloor is the HTTP status threshold at and above
// which a response counts as an error and gets routed through
// classifyResponse. Standard "4xx and 5xx are errors" convention.
const httpErrorStatusFloor = 400

// errBodyTruncateLen caps how much of a non-canonical error body
// we surface in StatusError.Message. Long enough to capture a
// meaningful prefix from an LB's HTML error page; short enough to
// keep logs sane.
const errBodyTruncateLen = 256

// failoverShuffler is the pluggable random source the do() path uses
// to randomize endpoint order. Wrapped in a struct so tests can
// inject a deterministic sequence (see useStaticOrder in tests).
type failoverShuffler struct {
	mu  sync.Mutex
	rng *mathrand.Rand
}

// newFailoverShuffler seeds a per-Client PCG so different Client
// instances in the same process don't all pick the same endpoint
// order. Crypto-seeded once at construction — fast and avoids the
// time-based seeding collisions clients sometimes fall into.
func newFailoverShuffler() *failoverShuffler {
	var seed [16]byte

	_, err := rand.Read(seed[:])
	if err != nil {
		// Should be impossible — crypto/rand failing means the
		// system is in deep trouble. Fall back to a sensible
		// non-zero seed so the client still works.
		seed = [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	}

	src := mathrand.NewPCG(
		binary.LittleEndian.Uint64(seed[:8]),
		binary.LittleEndian.Uint64(seed[8:]),
	)

	// Crypto-seeded math/rand is exactly the standard recipe for a
	// non-security-critical shuffle: we want unpredictable initial
	// state across processes (so a fleet of clients doesn't all
	// pick endpoint 0 first) without paying crypto/rand's per-call
	// cost on the hot path. The seed comes from crypto/rand;
	// downstream randomness has no security property attached.
	// #nosec G404 -- failover shuffle, not security-critical; seeded from crypto/rand
	return &failoverShuffler{rng: mathrand.New(src)}
}

// order returns a randomized index permutation of length n. The
// permutation is fresh per call so two concurrent goroutines don't
// share the same order and synchronize on the same endpoint.
func (s *failoverShuffler) order(n int) []int {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]int, n)
	for i := range out {
		out[i] = i
	}

	s.rng.Shuffle(n, func(i, j int) { out[i], out[j] = out[j], out[i] })

	return out
}

// do dispatches a request against the cluster with random-pick
// failover (F2 from RFC 0003). On transport failure or retryable
// status code (5xx / 503 draining) it walks to the next endpoint.
// On 4xx (auth, scope, not-found, bad-request) it returns
// immediately — those answers are deterministic and won't change
// on another endpoint.
//
// Returns an *http.Response on success; the caller is responsible
// for draining + closing the body. On exhaustive failure returns
// an error wrapping ErrAllEndpointsFailed with the last status seen.
func (c *Client) do(
	ctx context.Context,
	method, path string,
	body io.Reader,
	headers map[string]string,
) (*http.Response, error) {
	endpoints := c.currentEndpoints()
	if len(endpoints) == 0 {
		return nil, ErrNoEndpoints
	}

	// Cache the body bytes once if there is one — http.NewRequest
	// consumes the io.Reader, so retries against the next endpoint
	// need a fresh reader. We only buffer if there's a body; nil
	// reader stays nil.
	var bodyBytes []byte

	if body != nil {
		buffered, err := io.ReadAll(body)
		if err != nil {
			return nil, fmt.Errorf("buffer request body: %w", err)
		}

		bodyBytes = buffered
	}

	c.failoverRandMu.Lock()

	order := c.failoverRand.order(len(endpoints))
	c.failoverRandMu.Unlock()

	var lastErr error

	for _, i := range order {
		endpoint := endpoints[i]

		resp, err := c.tryOnce(ctx, endpoint, method, path, bodyBytes, headers)
		if err == nil {
			return resp, nil
		}

		if !isRetryable(err) {
			return nil, err
		}

		c.logger.Debug(
			"client failover",
			slog.String("endpoint", endpoint),
			slog.String("method", method),
			slog.String("path", path),
			slog.Any("err", err),
		)

		lastErr = err
	}

	return nil, fmt.Errorf("%w (%d endpoints tried): %w", ErrAllEndpointsFailed, len(endpoints), lastErr)
}

// tryOnce dispatches a single request against the given endpoint.
// Errors here are either *StatusError (4xx/5xx response) or a raw
// transport error (connection refused, TLS failure, etc.). The
// caller's failover policy (isRetryable) decides whether to move on.
func (c *Client) tryOnce(
	ctx context.Context,
	endpoint, method, path string,
	bodyBytes []byte,
	headers map[string]string,
) (*http.Response, error) {
	var body io.Reader

	if bodyBytes != nil {
		body = newRepeatableReader(bodyBytes)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint+path, body)
	if err != nil {
		return nil, fmt.Errorf("build %s %s: %w", method, path, err)
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dispatch %s %s to %s: %w", method, path, endpoint, err)
	}

	// 4xx and 5xx need classification; pass them up through the
	// classify path on the caller's side. Successful responses
	// (200, 204) flow through unchanged so the caller can decode
	// the body.
	if resp.StatusCode >= httpErrorStatusFloor {
		return nil, classifyResponse(resp)
	}

	return resp, nil
}

// currentEndpoints returns the working endpoint list, falling back
// to seeds when the refresh loop has produced an empty view. The
// fallback covers the partition-during-refresh case from RFC 0003
// open question 5: if all known endpoints are unreachable, the
// seeds remain as a permanent recovery anchor.
func (c *Client) currentEndpoints() []string {
	snap := c.endpoints.Load()
	if snap == nil || len(*snap) == 0 {
		return c.seeds
	}

	return *snap
}

// classifyResponse parses the cache's error envelope and returns a
// typed *StatusError. Consumes the response body. Falls back to a
// status-line-only StatusError when the body doesn't parse — that
// should only happen if a load balancer returns its own non-JSON
// 5xx ahead of the cache.
func classifyResponse(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	defer closeBody(resp)

	var env errorEnvelope

	parseErr := json.Unmarshal(body, &env)
	if parseErr != nil || env.Code == "" {
		// Body wasn't a canonical error envelope — keep the raw
		// status and message as a best-effort StatusError.
		return &StatusError{
			HTTPStatus: resp.StatusCode,
			Code:       "",
			Message:    truncate(string(body), errBodyTruncateLen),
		}
	}

	return &StatusError{
		HTTPStatus: resp.StatusCode,
		Code:       env.Code,
		Message:    env.Error,
		Details:    env.Details,
	}
}

// truncate caps a string for safe inclusion in error messages — we
// don't want a 5MB body crashing logs. 256 chars is enough to
// capture the meaningful prefix of a structured error and most
// stack-trace summaries.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[:n] + "..."
}

// newRepeatableReader returns an io.Reader over a byte slice. Used
// for failover retries since http.NewRequest consumes the original
// reader; we need a fresh reader for each attempt.
func newRepeatableReader(b []byte) io.Reader {
	return &repeatableReader{data: b}
}

type repeatableReader struct {
	data []byte
	pos  int
}

func (r *repeatableReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}

	n := copy(p, r.data[r.pos:])

	r.pos += n

	return n, nil
}
