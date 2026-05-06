package httpauth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeAuthYAML drops a YAML config in t.TempDir and returns its
// path. Failure here is a test-infra problem, never a Policy
// concern, so we t.Fatalf rather than t.Error.
func writeAuthYAML(t *testing.T, content string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "auth.yaml")

	err := os.WriteFile(path, []byte(content), 0o600)
	if err != nil {
		t.Fatalf("write tempfile: %v", err)
	}

	return path
}

// TestLoadFromEnv_Legacy pins the backwards-compat shortcut: when
// only HYPERCACHE_AUTH_TOKEN is set, the loader synthesizes a
// single all-scopes TokenIdentity. This is the legacy path every
// pre-v2 deployment relies on; if it ever breaks, every operator
// upgrading hits the wall on day one.
func TestLoadFromEnv_Legacy(t *testing.T) {
	// Cannot t.Parallel() — mutates process env. Same constraint
	// applies to every test in this file that touches Setenv.
	t.Setenv(EnvAuthConfig, "")
	t.Setenv(EnvAuthToken, "legacy-token")

	p, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}

	if len(p.Tokens) != 1 {
		t.Fatalf("len(Tokens) = %d, want 1", len(p.Tokens))
	}

	t0 := p.Tokens[0]
	if t0.ID != "default" {
		t.Errorf("ID = %q, want %q", t0.ID, "default")
	}

	if t0.Token != "legacy-token" {
		t.Errorf("Token = %q, want %q", t0.Token, "legacy-token")
	}

	want := []Scope{ScopeRead, ScopeWrite, ScopeAdmin}
	if len(t0.Scopes) != len(want) {
		t.Fatalf("Scopes = %v, want %v", t0.Scopes, want)
	}
}

// TestLoadFromEnv_BothCoexist verifies the loader treats
// EnvAuthConfig and EnvAuthToken as orthogonal — config wins for
// the client API, and EnvAuthToken stays available for the dist
// transport's symmetric peer auth. Concretely: when CONFIG points
// at a valid file, the returned Policy mirrors the file (it does
// NOT also include the legacy token), and no error fires.
//
// This is the standard production shape: a multi-tenant client API
// (config-driven) on top of a single-trust-domain cluster (one
// shared peer token).
func TestLoadFromEnv_BothCoexist(t *testing.T) {
	yaml := `
tokens:
  - id: from-file
    token: file-token
    scopes: [read]
`

	path := writeAuthYAML(t, yaml)

	t.Setenv(EnvAuthConfig, path)
	t.Setenv(EnvAuthToken, "dist-peer-token")

	p, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}

	if len(p.Tokens) != 1 || p.Tokens[0].ID != "from-file" {
		t.Fatalf("policy = %+v, want one token sourced from CONFIG (file wins)", p)
	}

	for _, tok := range p.Tokens {
		if tok.Token == "dist-peer-token" {
			t.Fatalf("legacy EnvAuthToken leaked into Policy.Tokens: %+v", tok)
		}
	}
}

// TestLoadFromEnv_Neither returns the zero Policy with no error
// when neither env is set. The hypercache-server caller is
// responsible for emitting the "running with no auth" warning.
func TestLoadFromEnv_Neither(t *testing.T) {
	t.Setenv(EnvAuthConfig, "")
	t.Setenv(EnvAuthToken, "")

	p, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}

	if p.IsConfigured() {
		t.Errorf("zero policy should not be configured: %+v", p)
	}
}

// TestLoadFromFile_Happy parses a complete config with both tokens
// and cert identities and pins each field through to the Policy.
func TestLoadFromFile_Happy(t *testing.T) {
	yaml := `
tokens:
  - id: app-prod
    token: app-prod-secret
    scopes: [read, write]
  - id: ops
    token: ops-secret
    scopes: [admin]
cert_identities:
  - subject_cn: app.internal
    scopes: [read]
allow_anonymous: false
`

	path := writeAuthYAML(t, yaml)

	t.Setenv(EnvAuthConfig, path)
	t.Setenv(EnvAuthToken, "")

	p, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}

	if len(p.Tokens) != 2 {
		t.Errorf("len(Tokens) = %d, want 2", len(p.Tokens))
	}

	if len(p.CertIdentities) != 1 {
		t.Errorf("len(CertIdentities) = %d, want 1", len(p.CertIdentities))
	}

	if p.AllowAnonymous {
		t.Errorf("AllowAnonymous = true, want false")
	}
}

// loadFailureCase is one row of the loader failure-mode table.
// Hoisted to package scope so the test body stays under the
// function-length lint threshold and adding a new failure case
// is a single literal append.
type loadFailureCase struct {
	name    string
	content string
	setup   func(t *testing.T) string
}

// loadFailureCases enumerates every input the loader must reject.
// The "secret-leak-canary" token body in every YAML is the
// regression target: each test asserts the resulting error message
// does NOT contain that substring, so a future wrap-error change
// that accidentally chains the file body into the message gets
// caught by the suite (not by the security audit).
//
//nolint:gochecknoglobals // test-only fixture table; sharing across subtests is the point
var loadFailureCases = []loadFailureCase{
	{
		name: "missing file",
		setup: func(_ *testing.T) string {
			return "/path/that/does/not/exist/auth.yaml"
		},
	},
	{
		name:    "malformed YAML",
		content: "tokens:\n  - id: app\n    token: x\n   bad-indent",
	},
	{
		name: "unknown field rejected (typo guard)",
		content: `
tokens:
  - id: x
    token: secret-leak-canary
    scopes: [read]
    unknown_field: typo
`,
	},
	{
		name: "unknown scope name",
		content: `
tokens:
  - id: x
    token: secret-leak-canary
    scopes: [readonly]
`,
	},
	{
		name: "empty scopes list",
		content: `
tokens:
  - id: x
    token: secret-leak-canary
    scopes: []
`,
	},
	{
		name: "empty token field",
		content: `
tokens:
  - id: x
    token: ""
    scopes: [read]
`,
	},
	{
		name: "empty ID field",
		content: `
tokens:
  - id: ""
    token: secret-leak-canary
    scopes: [read]
`,
	},
	{
		name: "empty cert subject_cn",
		content: `
cert_identities:
  - subject_cn: ""
    scopes: [read]
`,
	},
}

// TestLoadFromFile_FailureModes covers the load-time errors the
// loader is meant to fail-closed on. Each row asserts the error
// is non-nil AND the message does NOT include any token body —
// regression coverage for accidental secret leaks via wrap-error
// chains.
func TestLoadFromFile_FailureModes(t *testing.T) {
	for _, tc := range loadFailureCases {
		t.Run(tc.name, func(t *testing.T) {
			var path string

			if tc.setup != nil {
				path = tc.setup(t)
			} else {
				path = writeAuthYAML(t, tc.content)
			}

			t.Setenv(EnvAuthConfig, path)
			t.Setenv(EnvAuthToken, "")

			_, err := LoadFromEnv()
			if err == nil {
				t.Fatalf("expected an error")
			}

			if strings.Contains(err.Error(), "secret-leak-canary") {
				t.Fatalf("error leaks token body: %q", err.Error())
			}
		})
	}
}

// TestLoadFromFile_PrecedenceOverLegacy confirms the file path wins
// when EnvAuthConfig is set — except that "both set" is its own
// failure mode (TestLoadFromEnv_Both), so this only exercises
// "config set, token unset".
func TestLoadFromFile_PrecedenceOverLegacy(t *testing.T) {
	yaml := `
tokens:
  - id: from-file
    token: file-token
    scopes: [read]
`

	path := writeAuthYAML(t, yaml)

	t.Setenv(EnvAuthConfig, path)
	t.Setenv(EnvAuthToken, "")

	p, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}

	if len(p.Tokens) != 1 || p.Tokens[0].ID != "from-file" {
		t.Fatalf("policy = %+v, want one token from file", p)
	}
}
