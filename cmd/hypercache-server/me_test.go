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
				Capabilities: []string{capabilityCacheRead},
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
				Capabilities: []string{capabilityCacheRead, capabilityCacheWrite},
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
				Capabilities: []string{capabilityCacheRead, capabilityCacheWrite, capabilityCacheAdmin},
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

// TestHandleCan_AllowedAndDenied pins the canonical happy paths:
// an identity holding a capability gets allowed=true; the same
// identity probed against a capability it lacks gets allowed=false.
// Both produce 200 — "allowed=false" is a successful probe, not
// an authorization failure.
func TestHandleCan_AllowedAndDenied(t *testing.T) {
	t.Parallel()

	readWriteIdentity := httpauth.Identity{
		ID:     "ops-rw",
		Scopes: []httpauth.Scope{httpauth.ScopeRead, httpauth.ScopeWrite},
	}

	tests := []struct {
		name        string
		capability  string
		wantAllowed bool
	}{
		{"read holder asks for read", capabilityCacheRead, true},
		{"rw holder asks for write", capabilityCacheWrite, true},
		{"rw holder asks for admin", capabilityCacheAdmin, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			body := callCanWithIdentity(t, readWriteIdentity, tc.capability)
			if body.Capability != tc.capability {
				t.Errorf("capability echo: got %q, want %q", body.Capability, tc.capability)
			}

			if body.Allowed != tc.wantAllowed {
				t.Errorf("allowed: got %v, want %v", body.Allowed, tc.wantAllowed)
			}
		})
	}
}

// TestHandleCan_MissingCapabilityParam pins the input-validation
// posture: a request without the `capability` query param fails
// 400 with the canonical error envelope. We don't silently
// default to allowed=false — that would let typos pass as
// "you can't do it" when the real issue is the missing argument.
func TestHandleCan_MissingCapabilityParam(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals(httpauth.IdentityKey, httpauth.Identity{ID: "x", Scopes: []httpauth.Scope{httpauth.ScopeRead}})

		return c.Next()
	})
	app.Get("/v1/me/can", handleCan)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/me/can", strings.NewReader(""))

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", resp.StatusCode)
	}
}

// TestHandleCan_UnknownCapability pins that unrecognized
// capability strings fail 400, not silently allowed=false. A
// typo like `cache.reaad` should surface as a client error so
// the caller fixes their code rather than shipping a broken
// authz check.
func TestHandleCan_UnknownCapability(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals(httpauth.IdentityKey, httpauth.Identity{
			ID:     "x",
			Scopes: []httpauth.Scope{httpauth.ScopeRead, httpauth.ScopeWrite, httpauth.ScopeAdmin},
		})

		return c.Next()
	})
	app.Get("/v1/me/can", handleCan)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		"/v1/me/can?capability=cache.reaad", strings.NewReader(""))

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400 (unknown capability)", resp.StatusCode)
	}
}

// callCanWithIdentity drives /v1/me/can with a pre-populated
// IdentityKey local. Returns the decoded canResponse; failed
// status / decode trips the test fatally.
func callCanWithIdentity(t *testing.T, identity httpauth.Identity, capability string) canResponse {
	t.Helper()

	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals(httpauth.IdentityKey, identity)

		return c.Next()
	})
	app.Get("/v1/me/can", handleCan)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		"/v1/me/can?capability="+capability, strings.NewReader(""))

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}

	var got canResponse

	err = json.NewDecoder(resp.Body).Decode(&got)
	if err != nil {
		t.Fatalf("decode body: %v", err)
	}

	return got
}
