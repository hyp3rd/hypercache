package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	fiber "github.com/gofiber/fiber/v3"

	"github.com/hyp3rd/hypercache/pkg/httpauth"
)

// TestHandleMe_BodyShape pins the wire shape of GET /v1/me. We mount
// the handler behind a tiny inline middleware that stuffs a known
// Identity into Locals — the same shape httpauth.Policy.Middleware
// installs after a successful auth match — so the assertion is purely
// about the response body, not the auth resolver. auth_test.go covers
// the authentication contract; this test covers the rendering one.
func TestHandleMe_BodyShape(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		identity httpauth.Identity
		want     meResponse
	}{
		{
			name: "read-only operator",
			identity: httpauth.Identity{
				ID:     "ops-readonly",
				Scopes: []httpauth.Scope{httpauth.ScopeRead},
			},
			want: meResponse{
				ID:           "ops-readonly",
				Scopes:       []string{"read"},
				Capabilities: []string{"cache.read"},
			},
		},
		{
			name: "rw operator",
			identity: httpauth.Identity{
				ID:     "ops-rw",
				Scopes: []httpauth.Scope{httpauth.ScopeRead, httpauth.ScopeWrite},
			},
			want: meResponse{
				ID:           "ops-rw",
				Scopes:       []string{"read", "write"},
				Capabilities: []string{"cache.read", "cache.write"},
			},
		},
		{
			name: "anonymous (AllowAnonymous=true on the policy)",
			identity: httpauth.Identity{
				ID:     "anonymous",
				Scopes: []httpauth.Scope{httpauth.ScopeRead, httpauth.ScopeWrite, httpauth.ScopeAdmin},
			},
			want: meResponse{
				ID:           "anonymous",
				Scopes:       []string{"read", "write", "admin"},
				Capabilities: []string{"cache.read", "cache.write", "cache.admin"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := callMeWithIdentity(t, tc.identity)
			assertMeBody(t, got, tc.want)
		})
	}
}

// callMeWithIdentity drives /v1/me through a fiber app with a single
// middleware that pre-populates IdentityKey. Returns the decoded
// response body. Failed status / decode trips the test fatally.
func callMeWithIdentity(t *testing.T, identity httpauth.Identity) meResponse {
	t.Helper()

	app := fiber.New()
	// Stand-in for httpauth.Policy.Middleware — installs the
	// Locals entry handleMe reads. Test owns the identity it
	// asserts against.
	app.Use(func(c fiber.Ctx) error {
		c.Locals(httpauth.IdentityKey, identity)

		return c.Next()
	})
	app.Get("/v1/me", handleMe)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/me", strings.NewReader(""))

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}

	var got meResponse

	err = json.NewDecoder(resp.Body).Decode(&got)
	if err != nil {
		t.Fatalf("decode body: %v", err)
	}

	return got
}

// assertMeBody compares a decoded meResponse against the expected
// shape. ID is a single string compare; scopes are ordered slices
// (handleMe preserves the Identity.Scopes order, so order is part
// of the contract).
func assertMeBody(t *testing.T, got, want meResponse) {
	t.Helper()

	if got.ID != want.ID {
		t.Errorf("id: got %q, want %q", got.ID, want.ID)
	}

	if len(got.Scopes) != len(want.Scopes) {
		t.Fatalf("scopes length: got %d, want %d (got=%v)", len(got.Scopes), len(want.Scopes), got.Scopes)
	}

	for i, s := range want.Scopes {
		if got.Scopes[i] != s {
			t.Errorf("scopes[%d]: got %q, want %q", i, got.Scopes[i], s)
		}
	}

	if len(got.Capabilities) != len(want.Capabilities) {
		t.Fatalf("capabilities length: got %d, want %d (got=%v)", len(got.Capabilities), len(want.Capabilities), got.Capabilities)
	}

	for i, c := range want.Capabilities {
		if got.Capabilities[i] != c {
			t.Errorf("capabilities[%d]: got %q, want %q", i, got.Capabilities[i], c)
		}
	}
}

// TestHandleMe_MissingLocals covers the wiring-bug path. If a future
// refactor mounts handleMe without the auth middleware, the type
// assertion in handleMe falls through to a 500 with codeInternal —
// not a silent default identity. This test pins that fail-loud
// behavior so a misconfiguration cannot silently degrade to
// "everyone is anonymous, with all scopes.".
func TestHandleMe_MissingLocals(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	// No middleware installs IdentityKey — handleMe should 500.
	app.Get("/v1/me", handleMe)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/me", strings.NewReader(""))

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500", resp.StatusCode)
	}
}
