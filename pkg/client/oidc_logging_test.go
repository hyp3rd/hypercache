package client

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// stubTokenSource returns successive tokens from a pre-built
// queue. Used to drive the loggingTokenSource through rotation
// scenarios without an actual IdP.
type stubTokenSource struct {
	tokens []*oauth2.Token
	calls  atomic.Int64
}

func (s *stubTokenSource) Token() (*oauth2.Token, error) {
	n := s.calls.Add(1) - 1
	if int(n) >= len(s.tokens) {
		// Past the configured queue: return the last token
		// repeatedly (mimics oauth2's cached-token behavior).
		return s.tokens[len(s.tokens)-1], nil
	}

	return s.tokens[n], nil
}

// TestLoggingTokenSource_LogsOnRotation pins the canonical behavior:
// each time the underlying TokenSource returns a token whose
// Expiry differs from the previous one, the wrapper emits one
// "oidc token rotated" log line.
//
// Two distinct tokens → two log lines. No spam from cached
// returns (covered by the next test).
func TestLoggingTokenSource_LogsOnRotation(t *testing.T) {
	t.Parallel()

	now := time.Now()

	stub := &stubTokenSource{
		tokens: []*oauth2.Token{
			{AccessToken: "tok-1", TokenType: "Bearer", Expiry: now.Add(time.Hour)},
			{AccessToken: "tok-2", TokenType: "Bearer", Expiry: now.Add(2 * time.Hour)},
		},
	}

	buf := &bytes.Buffer{}
	c := &Client{logger: slog.New(slog.NewJSONHandler(buf, nil))}

	source := newLoggingTokenSource(stub, c)

	for range 2 {
		_, err := source.Token()
		if err != nil {
			t.Fatalf("Token: %v", err)
		}
	}

	lines := splitLines(buf.String())
	if len(lines) != 2 {
		t.Fatalf("want 2 log lines, got %d: %v", len(lines), lines)
	}

	for i, line := range lines {
		var rec map[string]any

		err := json.Unmarshal([]byte(line), &rec)
		if err != nil {
			t.Fatalf("line %d malformed: %v", i, err)
		}

		if rec["msg"] != "oidc token rotated" {
			t.Errorf("line %d msg: got %q, want %q", i, rec["msg"], "oidc token rotated")
		}
	}
}

// TestLoggingTokenSource_NoLogOnCachedToken pins the inverse:
// when the underlying source returns the same Expiry (the typical
// oauth2/clientcredentials path where the cached token is still
// valid), the wrapper stays silent. This is the contract that
// keeps log volume tied to actual rotations.
func TestLoggingTokenSource_NoLogOnCachedToken(t *testing.T) {
	t.Parallel()

	exp := time.Now().Add(time.Hour)
	stub := &stubTokenSource{
		tokens: []*oauth2.Token{
			{AccessToken: "cached", TokenType: "Bearer", Expiry: exp},
		},
	}

	buf := &bytes.Buffer{}
	c := &Client{logger: slog.New(slog.NewJSONHandler(buf, nil))}

	source := newLoggingTokenSource(stub, c)

	// 5 calls: first returns the token (rotation log fires), the
	// remaining four return the same cached token (no log).
	for range 5 {
		_, err := source.Token()
		if err != nil {
			t.Fatalf("Token: %v", err)
		}
	}

	lines := splitLines(buf.String())
	if len(lines) != 1 {
		t.Errorf("cached-token reuse should emit one rotation log, got %d:\n%s",
			len(lines), buf.String())
	}
}

// TestLoggingTokenSource_NilClientIsNoop documents the defensive
// path: even if the wrapper is given a nil Client (shouldn't
// happen via WithOIDCClientCredentials, but the constructor
// doesn't reject it), Token() must not panic. The rotation log
// is just skipped.
func TestLoggingTokenSource_NilClientIsNoop(t *testing.T) {
	t.Parallel()

	stub := &stubTokenSource{
		tokens: []*oauth2.Token{
			{AccessToken: "t", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)},
		},
	}

	source := newLoggingTokenSource(stub, nil)

	tok, err := source.Token()
	if err != nil {
		t.Fatalf("Token with nil client: %v", err)
	}

	if tok.AccessToken != "t" {
		t.Errorf("got token %q, want t", tok.AccessToken)
	}
}

// splitLines is a small helper to split a JSON-log buffer on
// newlines, dropping the final empty entry from the trailing \n.
func splitLines(s string) []string {
	parts := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(parts) == 1 && parts[0] == "" {
		return nil
	}

	return parts
}
