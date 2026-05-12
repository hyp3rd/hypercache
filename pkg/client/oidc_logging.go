package client

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// loggingTokenSource wraps an oauth2.TokenSource and emits an Info
// log line each time the underlying source returns a token whose
// expiry differs from the previous one — i.e. when a real refresh
// happened, not when the cache simply returned the still-valid
// token from memory.
//
// This closes RFC 0003 open question "Zero token-refresh
// visibility": before this wrapper, the OIDC source rotated tokens
// silently and operators debugging "why are my requests suddenly
// 401?" had no way to correlate request failures with token age.
// Now every rotation surfaces through the client's WithLogger
// surface alongside the other lifecycle events.
//
// The Client pointer is held by reference (not its logger field)
// so a WithLogger applied AFTER WithOIDCClientCredentials still
// reaches this wrapper — c.logger is read at rotation time, not
// at construction.
type loggingTokenSource struct {
	inner  oauth2.TokenSource
	client *Client

	mu      sync.Mutex
	lastExp time.Time
}

func newLoggingTokenSource(inner oauth2.TokenSource, c *Client) oauth2.TokenSource {
	return &loggingTokenSource{inner: inner, client: c}
}

// Token implements oauth2.TokenSource. Delegates to the inner
// source and logs when the returned token's Expiry differs from
// the most-recently-observed one. Cached tokens (same Expiry)
// produce no log noise — every line in the log corresponds to a
// real IdP rotation.
func (l *loggingTokenSource) Token() (*oauth2.Token, error) {
	tok, err := l.inner.Token()
	if err != nil {
		return nil, fmt.Errorf("oidc token source: %w", err)
	}

	l.mu.Lock()

	rotated := l.lastExp.IsZero() || !l.lastExp.Equal(tok.Expiry)

	l.lastExp = tok.Expiry
	l.mu.Unlock()

	if rotated && l.client != nil && l.client.logger != nil {
		l.client.logger.Info(
			"oidc token rotated",
			slog.Time("expires_at", tok.Expiry),
			slog.String("token_type", tok.TokenType),
		)
	}

	return tok, nil
}
