package httpauth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fiber "github.com/gofiber/fiber/v3"
)

// errVerifyRejected is the canonical "ServerVerify said no" sentinel
// the policy_test stubs return. Defining it as a static error
// dodges err113 without reaching for fmt.Errorf in test bodies.
var errVerifyRejected = errors.New("rejected")

// newTestApp wires a single auth-protected route and returns the
// fiber app for in-memory request driving. The route returns 200
// with "ok" so the test bodies only need to assert status codes —
// auth is what's under test, not handler logic.
func newTestApp(t *testing.T, p Policy, scope Scope) *fiber.App {
	t.Helper()

	app := fiber.New()
	app.Get("/protected", p.Middleware(scope), func(c fiber.Ctx) error { return c.SendString("ok") })

	return app
}

// doStatus issues the request and returns just the status code.
// Auth assertions never need the body, so this keeps the test
// table-row bodies one line each.
func doStatus(t *testing.T, app *fiber.App, header string) int {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/protected", strings.NewReader(""))
	if header != "" {
		req.Header.Set("Authorization", header)
	}

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode
}

// TestPolicy_Bearer drives the multi-token + scope path end-to-end
// through fiber.App.Test. Each row is one (header, required scope,
// expected status) tuple. The shared policy carries two identities
// — a read-only token and a read+write token — so the same policy
// can be checked against both Read-required and Write-required
// routes per case.
func TestPolicy_Bearer(t *testing.T) {
	t.Parallel()

	p := Policy{
		Tokens: []TokenIdentity{
			{ID: "ro", Token: "read-only-token", Scopes: []Scope{ScopeRead}},
			{ID: "rw", Token: "read-write-token", Scopes: []Scope{ScopeRead, ScopeWrite}},
		},
	}

	tests := []struct {
		name   string
		header string
		scope  Scope
		want   int
	}{
		{"no header → 401", "", ScopeRead, http.StatusUnauthorized},
		{"unknown token → 401", "Bearer nope", ScopeRead, http.StatusUnauthorized},
		{"missing Bearer prefix → 401", "read-only-token", ScopeRead, http.StatusUnauthorized},
		{"ro token + Read scope → 200", "Bearer read-only-token", ScopeRead, http.StatusOK},
		{"ro token + Write scope → 403", "Bearer read-only-token", ScopeWrite, http.StatusForbidden},
		{"rw token + Write scope → 200", "Bearer read-write-token", ScopeWrite, http.StatusOK},
		{"rw token + Admin scope → 403", "Bearer read-write-token", ScopeAdmin, http.StatusForbidden},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app := newTestApp(t, p, tc.scope)

			got := doStatus(t, app, tc.header)
			if got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
}

// TestPolicy_BearerVisitsAllTokens pins the timing-leak guardrail
// from policy.go's resolveBearer comment: the loop must not break
// on early match, so the count of configured tokens does not leak
// via wall-clock duration.
//
// We can't directly assert "ran in constant time" portably, but we
// CAN assert the matched token is the LAST configured token —
// proving the loop continued past earlier matches. If a future
// contributor adds an early `break`, the loop would short-circuit
// after the first match and matching the duplicate-but-later token
// would not be possible.
//
// (The duplicate is constructed deliberately so two configured
// identities accept the same header. resolveBearer's contract is
// "last writer wins" by virtue of the no-break loop; that's what
// we pin here.)
func TestPolicy_BearerVisitsAllTokens(t *testing.T) {
	t.Parallel()

	p := Policy{
		Tokens: []TokenIdentity{
			{ID: "first", Token: "shared-token", Scopes: []Scope{ScopeRead}},
			{ID: "second", Token: "shared-token", Scopes: []Scope{ScopeRead, ScopeWrite}},
		},
	}

	identity, ok := p.resolveBearer("Bearer shared-token")
	if !ok {
		t.Fatalf("expected a match")
	}

	// If the loop short-circuited on first match, we'd see "first"
	// here. The no-early-break invariant guarantees we see "second".
	if identity.ID != "second" {
		t.Fatalf("identity.ID = %q, want %q (proves loop continues past matches; see resolveBearer comment)", identity.ID, "second")
	}
}

// TestPolicy_NoCredentialsFailsClosed asserts the fail-closed
// default: a Policy with no configured credential class refuses
// every request unless AllowAnonymous is explicitly set. This is
// the difference between dev-mode and prod-mode posture.
func TestPolicy_NoCredentialsFailsClosed(t *testing.T) {
	t.Parallel()

	app := newTestApp(t, Policy{}, ScopeRead)

	got := doStatus(t, app, "Bearer anything")
	if got != http.StatusUnauthorized {
		t.Fatalf("zero policy: got %d, want %d", got, http.StatusUnauthorized)
	}
}

// TestPolicy_AllowAnonymous opens the route to credential-free
// callers. Anonymous identity carries all three scopes — the
// AllowAnonymous flag is the operator's explicit opt-in to
// permissive mode (used by the binary's zero-config dev posture)
// and refusing scoped routes would 403 every legacy
// `docker run hypercache` without a paired auth config.
func TestPolicy_AllowAnonymous(t *testing.T) {
	t.Parallel()

	p := Policy{AllowAnonymous: true}

	scopes := []Scope{"", ScopeRead, ScopeWrite, ScopeAdmin}
	for _, s := range scopes {
		t.Run("scope="+string(s), func(t *testing.T) {
			t.Parallel()

			app := newTestApp(t, p, s)

			if got := doStatus(t, app, ""); got != http.StatusOK {
				t.Fatalf("scope=%q: got %d, want 200", s, got)
			}
		})
	}
}

// TestPolicy_ServerVerify exercises the universal escape hatch.
// Bearer fails (no tokens configured), cert fails (no TLS), and
// ServerVerify is the last resort. When it returns nil err, the
// caller is authorized; when it returns an error, the request 401s.
func TestPolicy_ServerVerify(t *testing.T) {
	t.Parallel()

	p := Policy{
		ServerVerify: func(c fiber.Ctx) (Identity, error) {
			if c.Get("X-Custom-Auth") == "yes" {
				return Identity{ID: "custom", Scopes: []Scope{ScopeRead}}, nil
			}

			return Identity{}, errVerifyRejected
		},
	}

	app := fiber.New()
	app.Get("/protected", p.Middleware(ScopeRead), func(c fiber.Ctx) error { return c.SendString("ok") })

	t.Run("hook accepts → 200", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/protected", strings.NewReader(""))
		req.Header.Set("X-Custom-Auth", "yes")

		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("app.Test: %v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("got %d, want 200", resp.StatusCode)
		}
	})

	t.Run("hook rejects → 401", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/protected", strings.NewReader(""))

		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("app.Test: %v", err)
		}

		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("got %d, want 401", resp.StatusCode)
		}
	})
}

// TestPolicy_IdentityInLocals proves the identity is reachable
// from downstream handlers via fiber.Ctx.Locals(IdentityKey).
// This is the hook that future audit/metrics code will use to
// attribute calls to principals.
func TestPolicy_IdentityInLocals(t *testing.T) {
	t.Parallel()

	p := Policy{
		Tokens: []TokenIdentity{
			{ID: "audit-target", Token: "tok", Scopes: []Scope{ScopeRead}},
		},
	}

	app := fiber.New()
	app.Get("/who", p.Middleware(ScopeRead), func(c fiber.Ctx) error {
		v := c.Locals(IdentityKey)

		id, ok := v.(Identity)
		if !ok {
			return c.Status(http.StatusInternalServerError).SendString("no identity")
		}

		return c.SendString(id.ID)
	})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/who", strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer tok")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	body := make([]byte, 64)

	n, _ := resp.Body.Read(body)

	got := string(body[:n])
	if got != "audit-target" {
		t.Fatalf("locals identity ID = %q, want %q", got, "audit-target")
	}
}

// TestPolicy_Validate covers the load-time coherence checks. Cases
// here also pin the messages do NOT include token bodies — a future
// edit that does would leak secrets to anyone reading startup logs.
func TestPolicy_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		policy    Policy
		wantErr   bool
		mustNotIn string // substring that must not appear in any error
	}{
		{
			name:    "zero policy is valid (open mode)",
			policy:  Policy{},
			wantErr: false,
		},
		{
			name: "valid token policy",
			policy: Policy{
				Tokens: []TokenIdentity{{ID: "x", Token: "secret", Scopes: []Scope{ScopeRead}}},
			},
			wantErr: false,
		},
		{
			name: "empty token errors and does not leak ID-as-secret",
			policy: Policy{
				Tokens: []TokenIdentity{{ID: "missing-token-id", Token: "", Scopes: []Scope{ScopeRead}}},
			},
			wantErr:   true,
			mustNotIn: "", // ID is fine to log; only secrets are.
		},
		{
			name: "empty ID errors without leaking the token",
			policy: Policy{
				//nolint:gosec // test-fixture string, not a credential
				Tokens: []TokenIdentity{
					{ID: "", Token: "leaked-if-included", Scopes: []Scope{ScopeRead}},
				},
			},
			wantErr:   true,
			mustNotIn: "leaked-if-included",
		},
		{
			name: "empty cert subject errors",
			policy: Policy{
				CertIdentities: []CertIdentity{{SubjectCN: "", Scopes: []Scope{ScopeRead}}},
			},
			wantErr: true,
		},
		{
			// AllowAnonymous-without-credentials is the legacy
			// "open dev mode" the binary falls back to when no
			// auth env vars are set. Validate must accept it; the
			// "running with no auth" warning is the binary's job.
			name:    "AllowAnonymous with no creds is valid (open dev mode)",
			policy:  Policy{AllowAnonymous: true},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.policy.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}

			if tc.mustNotIn != "" && err != nil && strings.Contains(err.Error(), tc.mustNotIn) {
				t.Fatalf("error message %q contains forbidden substring %q (would leak secret)", err.Error(), tc.mustNotIn)
			}
		})
	}
}

// TestPolicy_HasScope is a tiny cheap unit covering the inclusive
// (non-hierarchical) scope semantics — the deliberate counterpart
// to the fact that ScopeWrite does NOT imply ScopeRead.
func TestPolicy_HasScope(t *testing.T) {
	t.Parallel()

	id := Identity{Scopes: []Scope{ScopeWrite}}

	if id.HasScope(ScopeRead) {
		t.Errorf("Write should not imply Read (inclusive-not-hierarchical scope model)")
	}

	if !id.HasScope(ScopeWrite) {
		t.Errorf("Write should match Write")
	}
}
