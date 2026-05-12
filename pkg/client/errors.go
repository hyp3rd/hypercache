package client

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/hyp3rd/ewrap"
)

// Sentinel errors returned by the client. Use errors.Is to match
// them — every command method wraps its underlying *StatusError so
// `errors.Is(err, client.ErrNotFound)` is the canonical detection
// shape regardless of HTTP status mapping changes upstream.
//
// The sentinel set is intentionally small and stable. New conditions
// either map to an existing sentinel (the recommended path) or to
// the typed StatusError below for cases that need finer
// discrimination.
var (
	// ErrNotFound is returned when a key is missing from the cache,
	// or when a node-scoped resource (e.g. /v1/me on a misrouted
	// request) doesn't exist on the selected endpoint.
	ErrNotFound = ewrap.New("hypercache: key not found")
	// ErrUnauthorized means the credentials presented (bearer,
	// Basic, OIDC token, or cert) were not accepted by the cluster.
	// Caller must rotate credentials or re-authenticate; retrying
	// the same call against another endpoint won't help.
	ErrUnauthorized = ewrap.New("hypercache: unauthorized")
	// ErrForbidden means the credentials resolved to an identity
	// but the identity's scopes don't satisfy the route's scope
	// requirement. Caller needs a credential with the missing scope.
	ErrForbidden = ewrap.New("hypercache: forbidden")
	// ErrDraining means the targeted node is draining (preparing
	// for shutdown / rolling deploy). The client retries on the
	// next endpoint automatically; this sentinel surfaces only
	// when EVERY endpoint reports draining at once — i.e. the
	// entire cluster is mid-rolling-deploy.
	ErrDraining = ewrap.New("hypercache: cluster draining")
	// ErrBadRequest means the server rejected the request shape
	// (malformed key, invalid TTL string, unparseable body). Not
	// retryable — the caller has a bug to fix.
	ErrBadRequest = ewrap.New("hypercache: bad request")
	// ErrInternal wraps the cluster's INTERNAL/500 error class. The
	// client surfaces it as a sentinel so retry-with-backoff
	// helpers can distinguish "server is broken, try later" from
	// auth/scope/not-found.
	ErrInternal = ewrap.New("hypercache: internal server error")
	// ErrAllEndpointsFailed is returned when failover exhausted
	// every known endpoint without a successful response. The
	// underlying causes (network errors, 5xx, draining) are
	// preserved via fmt.Errorf wrapping; use errors.As against
	// *StatusError or net.Error if you need the original.
	ErrAllEndpointsFailed = ewrap.New("hypercache: all endpoints failed")
	// ErrNoEndpoints is returned when New is called with an empty
	// endpoint slice. The constructor catches this; runtime paths
	// fall back to the original seed list, so this only surfaces
	// at construction time.
	ErrNoEndpoints = ewrap.New("hypercache: at least one endpoint required")
)

// StatusError carries the cache's canonical error envelope (the
// `{ code, error, details }` JSON shape every 4xx/5xx returns)
// plus the HTTP status. Use errors.As to extract it for fields
// the sentinels don't capture (Details, the original Code string).
//
// StatusError implements `Is(target error) bool` so a wrapped
// StatusError still matches the family sentinels — `errors.Is(err,
// ErrNotFound)` returns true whether the caller got the raw
// *StatusError back or a wrapped error.
type StatusError struct {
	// HTTPStatus is the response status (e.g. 404, 401, 503).
	HTTPStatus int
	// Code is the canonical machine-readable identifier from the
	// server's error envelope (e.g. "NOT_FOUND", "DRAINING",
	// "UNAUTHORIZED", "INTERNAL", "BAD_REQUEST"). Stable across
	// server versions; clients should key off this rather than
	// HTTPStatus alone.
	Code string
	// Message is the human-readable summary the server emitted.
	// Safe to log; never contains secrets.
	Message string
	// Details is an optional free-form field carrying context
	// (e.g. "key 'foo' has invalid character at offset 3"). May
	// be empty.
	Details string
}

// Error renders the status, code, and message into a single string.
// Format is stable enough for log scraping but not a public API
// contract; structured-logging users should pull the fields off
// the StatusError directly.
func (e *StatusError) Error() string {
	if e == nil {
		return "<nil>"
	}

	if e.Details != "" {
		return fmt.Sprintf("hypercache: %d [%s]: %s (%s)", e.HTTPStatus, e.Code, e.Message, e.Details)
	}

	return fmt.Sprintf("hypercache: %d [%s]: %s", e.HTTPStatus, e.Code, e.Message)
}

// Is implements the errors.Is contract: a wrapped *StatusError
// matches the corresponding family sentinel based on the canonical
// Code string (preferred) or HTTPStatus (fallback for codes we
// don't recognize).
func (e *StatusError) Is(target error) bool {
	if e == nil {
		return target == nil
	}

	switch target {
	case ErrNotFound:
		return e.Code == "NOT_FOUND" || e.HTTPStatus == http.StatusNotFound
	case ErrUnauthorized:
		return e.Code == "UNAUTHORIZED" || e.HTTPStatus == http.StatusUnauthorized
	case ErrForbidden:
		return e.HTTPStatus == http.StatusForbidden
	case ErrDraining:
		return e.Code == "DRAINING" || e.HTTPStatus == http.StatusServiceUnavailable
	case ErrBadRequest:
		return e.Code == "BAD_REQUEST" || e.HTTPStatus == http.StatusBadRequest
	case ErrInternal:
		return e.Code == "INTERNAL" || (e.HTTPStatus >= 500 && e.HTTPStatus != http.StatusServiceUnavailable)
	}

	return false
}

// isRetryable reports whether an error returned by a single-endpoint
// request should trigger failover to the next endpoint. The failover
// policy is conservative: we retry on transport failures (the
// endpoint is unreachable or returned an unexpected wire shape) and
// on 5xx + 503 draining (the endpoint is up but unhealthy), but NOT
// on 4xx — a 401/403/400/404 is a deterministic answer and trying
// another endpoint won't change it.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}

	var se *StatusError

	if errors.As(err, &se) {
		// 5xx and 503 retry; other 4xx are terminal. 503/draining
		// is special-cased: even when the server is technically
		// returning a valid response, the right thing to do is
		// move on to the next endpoint.
		return se.HTTPStatus >= 500 || se.HTTPStatus == http.StatusServiceUnavailable
	}

	// Non-StatusError errors come from the transport layer:
	// connection refused, timeout, TLS handshake failure, etc.
	// Always retry these — they indicate the endpoint isn't
	// answering at all, which is exactly the case failover exists
	// to handle.
	return true
}
