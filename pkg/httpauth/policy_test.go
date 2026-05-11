package httpauth

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fiber "github.com/gofiber/fiber/v3"
	"github.com/hyp3rd/ewrap"
	"golang.org/x/crypto/bcrypt"
)

// bcryptTestCost is the bcrypt cost factor we use for fixtures.
// Cost 4 is bcrypt's minimum; production keys go at cost 10+. Using
// the minimum here keeps the test suite under a second total even
// across many bcrypt-verifying cases.
const bcryptTestCost = 4

// mustBcrypt hashes a plaintext password at bcryptTestCost. Test
// helper — panics on failure because a bcrypt failure with valid
// input is a test-rig bug, not a runtime condition to handle.
func mustBcrypt(t *testing.T, plaintext string) []byte {
	t.Helper()

	h, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcryptTestCost)
	if err != nil {
		t.Fatalf("bcrypt.GenerateFromPassword: %v", err)
	}

	return h
}

// basicHeader builds an `Authorization: Basic ...` header value for
// the given credentials. Keeps test rows short.
func basicHeader(username, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
}

// errVerifyRejected is the canonical "ServerVerify said no" sentinel
// the policy_test stubs return. Defining it as a static error
// dodges err113 without reaching for fmt.Errorf in test bodies.
var errVerifyRejected = ewrap.New("rejected")

// userAlice is the canonical username used across the Basic-auth
// test rows. Defined as a const so goconst is happy and renaming
// is a single edit.
const userAlice = "alice"

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

// TestPolicy_Verify covers the public Verify() entry point — the
// "block-with-error" sibling of Middleware that adapters
// (ManagementHTTPServer.WithMgmtControlAuth, etc.) use when they
// own their own next-handler dispatch. Same semantics as
// Middleware: 401 on missing/invalid creds, 403 on wrong scope,
// nil + Identity in Locals on success. The shared resolve() means
// any future divergence between Middleware and Verify would be a
// security bug; this test pins parity.
func TestPolicy_Verify(t *testing.T) {
	t.Parallel()

	p := Policy{
		Tokens: []TokenIdentity{
			{ID: "ro", Token: "ro-token", Scopes: []Scope{ScopeRead}},
			{ID: "admin", Token: "admin-token", Scopes: []Scope{ScopeRead, ScopeWrite, ScopeAdmin}},
		},
	}

	cases := []struct {
		name   string
		header string
		scope  Scope
		want   int
	}{
		{"no creds → 401", "", ScopeRead, http.StatusUnauthorized},
		{"bad bearer → 401", "Bearer wrong", ScopeRead, http.StatusUnauthorized},
		{"read scope on read route → 200", "Bearer ro-token", ScopeRead, http.StatusOK},
		{"read scope on admin route → 403", "Bearer ro-token", ScopeAdmin, http.StatusForbidden},
		{"admin scope on admin route → 200", "Bearer admin-token", ScopeAdmin, http.StatusOK},
		{"empty scope (any-authenticated) → 200 with creds", "Bearer ro-token", "", http.StatusOK},
		{"empty scope still 401 without creds", "", "", http.StatusUnauthorized},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app := fiber.New()
			// Mount Verify in the wrapAuth-style adapter shape:
			// auth happens, then handler runs only on nil error.
			// This is exactly how ManagementHTTPServer wires it.
			scope := tc.scope
			app.Get("/protected", func(c fiber.Ctx) error {
				err := p.Verify(c, scope)
				if err != nil {
					return err
				}

				return c.SendString("ok")
			})

			got := doStatus(t, app, tc.header)
			if got != tc.want {
				t.Fatalf("status: got %d, want %d", got, tc.want)
			}
		})
	}
}

// TestPolicy_Verify_StoresIdentityInLocals pins the side-effect
// contract: a successful Verify populates IdentityKey before
// returning. Adapters that read c.Locals(IdentityKey) — e.g. any
// future audit-attribution handler on the mgmt port — depend on
// it. Without this assertion a future refactor could regress
// Verify into "scope-check only" silently.
func TestPolicy_Verify_StoresIdentityInLocals(t *testing.T) {
	t.Parallel()

	p := Policy{
		Tokens: []TokenIdentity{
			{ID: "audit-target", Token: "tok", Scopes: []Scope{ScopeRead}},
		},
	}

	app := fiber.New()
	app.Get("/who", func(c fiber.Ctx) error {
		err := p.Verify(c, ScopeRead)
		if err != nil {
			return err
		}

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

// TestPolicy_Basic exercises the resolveBasic happy path and the
// most-frequent reject paths against a real Policy.Middleware chain.
// AllowBasicWithoutTLS is set true here because fiber.App.Test
// delivers plaintext requests; the fail-closed TLS posture is pinned
// separately in TestPolicy_BasicRefusesPlaintextByDefault.
func TestPolicy_Basic(t *testing.T) {
	t.Parallel()

	alicePassword := "correct-horse-battery-staple"
	bobPassword := "another-good-password"

	p := Policy{
		BasicIdentities: []BasicIdentity{
			{
				Username:       userAlice,
				PasswordBcrypt: mustBcrypt(t, alicePassword),
				ID:             userAlice,
				Scopes:         []Scope{ScopeRead},
			},
			{
				Username:       "bob",
				PasswordBcrypt: mustBcrypt(t, bobPassword),
				ID:             "bob",
				Scopes:         []Scope{ScopeRead, ScopeWrite},
			},
		},
		AllowBasicWithoutTLS: true,
	}

	tests := []struct {
		name   string
		header string
		scope  Scope
		want   int
	}{
		{"no header → 401", "", ScopeRead, http.StatusUnauthorized},
		{"unknown user → 401", basicHeader("eve", alicePassword), ScopeRead, http.StatusUnauthorized},
		{"alice + wrong password → 401", basicHeader(userAlice, "wrong"), ScopeRead, http.StatusUnauthorized},
		{"alice + correct password + Read → 200", basicHeader(userAlice, alicePassword), ScopeRead, http.StatusOK},
		{"alice + correct password + Write → 403", basicHeader(userAlice, alicePassword), ScopeWrite, http.StatusForbidden},
		{"bob + correct password + Write → 200", basicHeader("bob", bobPassword), ScopeWrite, http.StatusOK},
		{"bob + correct password + Admin → 403", basicHeader("bob", bobPassword), ScopeAdmin, http.StatusForbidden},
		{"malformed base64 → 401", "Basic !!!!", ScopeRead, http.StatusUnauthorized},
		{"missing colon → 401", "Basic " + base64.StdEncoding.EncodeToString([]byte("aliceonly")), ScopeRead, http.StatusUnauthorized},
		{"empty Basic → 401", "Basic ", ScopeRead, http.StatusUnauthorized},
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

// TestPolicy_BasicRefusesPlaintextByDefault pins the fail-closed
// posture: when AllowBasicWithoutTLS is false (the default) AND the
// request arrived over plaintext, resolveBasic returns false and the
// middleware 401s — even if the credentials would otherwise have
// matched. This protects operators who forget TLS in production from
// silently broadcasting passwords on the network.
func TestPolicy_BasicRefusesPlaintextByDefault(t *testing.T) {
	t.Parallel()

	password := "secure-but-doomed-in-plaintext"

	p := Policy{
		BasicIdentities: []BasicIdentity{
			{
				Username:       userAlice,
				PasswordBcrypt: mustBcrypt(t, password),
				ID:             userAlice,
				Scopes:         []Scope{ScopeRead},
			},
		},
		// AllowBasicWithoutTLS NOT set — we want the default behavior.
	}

	app := newTestApp(t, p, ScopeRead)

	got := doStatus(t, app, basicHeader(userAlice, password))
	if got != http.StatusUnauthorized {
		t.Fatalf("plaintext Basic must 401 by default; got %d", got)
	}
}

// TestPolicy_BearerWinsOverBasic pins the chain order: bearer
// resolution runs before Basic, so a request with a valid bearer
// resolves to the bearer's identity regardless of any Basic
// configuration. We read identity.ID back from c.Locals to make the
// determinism explicit — if a future contributor swaps the chain
// order, this test fails with a clear "got basic-id, want bearer-id"
// message.
func TestPolicy_BearerWinsOverBasic(t *testing.T) {
	t.Parallel()

	password := "alice-pass"

	p := Policy{
		Tokens: []TokenIdentity{
			{ID: "bearer-id", Token: "tok", Scopes: []Scope{ScopeRead}},
		},
		BasicIdentities: []BasicIdentity{
			{
				Username:       userAlice,
				PasswordBcrypt: mustBcrypt(t, password),
				ID:             "basic-id",
				Scopes:         []Scope{ScopeRead},
			},
		},
		AllowBasicWithoutTLS: true,
	}

	app := fiber.New()
	app.Get("/who", p.Middleware(ScopeRead), func(c fiber.Ctx) error {
		id, ok := c.Locals(IdentityKey).(Identity)
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
	if got != "bearer-id" {
		t.Fatalf("bearer must win over basic; got identity ID %q, want %q", got, "bearer-id")
	}
}

// TestIdentity_Capabilities pins the scope → capability mapping
// surface that /v1/me exposes to clients. The 1:1 prefix-with-cache.
// mapping is the v1 contract; if we ever break it (splitting a scope
// across multiple capabilities) this test is the canary.
func TestIdentity_Capabilities(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		scopes []Scope
		want   []string
	}{
		{"empty", nil, []string{}},
		{"read only", []Scope{ScopeRead}, []string{"cache.read"}},
		{"read+write", []Scope{ScopeRead, ScopeWrite}, []string{"cache.read", "cache.write"}},
		{"all three", []Scope{ScopeRead, ScopeWrite, ScopeAdmin}, []string{"cache.read", "cache.write", "cache.admin"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			id := Identity{ID: "test", Scopes: tc.scopes}

			got := id.Capabilities()
			if !sliceEq(got, tc.want) {
				t.Fatalf("Capabilities() = %v, want %v", got, tc.want)
			}
		})
	}
}

func sliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}
