package httpauth

import (
	"fmt"
	"os"

	"github.com/hyp3rd/ewrap"
	"gopkg.in/yaml.v3"
)

// Env-var names that drive policy loading. Kept as exported
// constants so the hypercache-server binary's documentation,
// the loader, and the tests all reference one canonical name.
const (
	// EnvAuthConfig points at a YAML file describing tokens
	// and cert identities. Takes precedence over EnvAuthToken.
	EnvAuthConfig = "HYPERCACHE_AUTH_CONFIG"
	// EnvAuthToken is the legacy single-token shortcut: when
	// set, the loader synthesizes one all-scopes TokenIdentity
	// with ID "default" so existing zero-config deployments
	// keep working byte-identical.
	EnvAuthToken = "HYPERCACHE_AUTH_TOKEN" // #nosec G101 -- env-var name, not a credential value
)

// Static loader errors. Wrapped with %w so callers get both
// errors.Is matching for control-flow and the contextual message
// (file path, field name) via Error(). Token bodies are NEVER
// included in any wrap — see fmt.Errorf sites below.
var (
	errEmptyScopes  = ewrap.New("scopes is empty; at least one of read/write/admin required")
	errUnknownScope = ewrap.New("unknown scope (valid: read, write, admin)")
)

// fileSchema is the YAML wire shape of HYPERCACHE_AUTH_CONFIG.
// Field tags use snake_case to match the project's YAML aesthetic
// (mkdocs.yml, redocly.yaml, openapi.yaml all snake_case).
//
// Wire example:
//
//	tokens:
//	  - id: app-prod
//	    token: "<secret>"
//	    scopes: [read, write]
//	cert_identities:
//	  - subject_cn: app.internal
//	    scopes: [read]
//	allow_anonymous: false
//
// Unrecognized fields are rejected via yaml.Decoder.KnownFields(true)
// in load() — typos in scope names or field names should fail loudly,
// not silently drop the misnamed identity.
type fileSchema struct {
	Tokens         []tokenFile `yaml:"tokens"`
	CertIdentities []certFile  `yaml:"cert_identities"`
	AllowAnonymous bool        `yaml:"allow_anonymous"`
}

type tokenFile struct {
	ID     string   `yaml:"id"`
	Token  string   `yaml:"token"`
	Scopes []string `yaml:"scopes"`
}

type certFile struct {
	SubjectCN string   `yaml:"subject_cn"`
	Scopes    []string `yaml:"scopes"`
}

// LoadFromEnv resolves a client-API auth Policy from the process
// environment. Precedence:
//
//  1. EnvAuthConfig set → load multi-token + cert-identity Policy
//     from the YAML file. Missing or malformed file returns an
//     error; the caller exits non-zero. This is a behavioral break
//     vs the legacy "missing token = open mode" posture and is
//     documented in CHANGELOG.
//  2. EnvAuthToken set → synthesize a single TokenIdentity with all
//     three scopes. Mirrors the pre-v2 behavior where one token
//     gated every protected route, so existing zero-config
//     deployments keep working byte-identical.
//  3. Neither set → return the zero Policy. Caller should log a
//     "running with no auth" warning and decide whether to opt
//     into AllowAnonymous mode.
//
// EnvAuthConfig and EnvAuthToken are NOT mutually exclusive: the
// dist transport's symmetric peer auth still reads EnvAuthToken
// directly (see cmd/hypercache-server/main.go's buildHyperCache).
// When both are set, EnvAuthConfig wins for the client API and
// EnvAuthToken is reused for dist — this is the standard config
// for a multi-tenant client API on top of a single-trust-domain
// cluster.
//
// LoadFromEnv runs Policy.Validate before returning so the caller
// gets a single error path; misconfigured policies surface as
// errors here rather than as silent runtime auth bypasses.
func LoadFromEnv() (Policy, error) {
	configPath := os.Getenv(EnvAuthConfig)
	legacyToken := os.Getenv(EnvAuthToken)

	if configPath != "" {
		return loadFromFile(configPath)
	}

	if legacyToken != "" {
		return synthesizeLegacyPolicy(legacyToken), nil
	}

	return Policy{}, nil
}

// synthesizeLegacyPolicy mirrors the pre-v2 single-token behavior:
// one token grants every scope. ID "default" is what shows up in
// future audit logs / Identity.ID for callers using the legacy env.
func synthesizeLegacyPolicy(token string) Policy {
	return Policy{
		Tokens: []TokenIdentity{
			{
				ID:     "default",
				Token:  token,
				Scopes: []Scope{ScopeRead, ScopeWrite, ScopeAdmin},
			},
		},
	}
}

// loadFromFile parses the YAML config and returns a validated Policy.
// Read errors (missing file, permission denied) and parse errors are
// wrapped with ErrInvalidPolicy so callers can distinguish bad-config
// from other fatal conditions. The token strings inside the file are
// NEVER included in any error message — wrapper messages mention
// only field names and IDs.
func loadFromFile(path string) (Policy, error) {
	// #nosec G304 G703 -- path is from operator-supplied HYPERCACHE_AUTH_CONFIG env var; the env is the trusted boundary, the same posture as every
	// other config-file load in the binary.
	f, err := os.Open(path)
	if err != nil {
		// os.PathError already contains the path; don't re-wrap.
		return Policy{}, fmt.Errorf("%w: %w", ErrInvalidPolicy, err)
	}

	defer func() { _ = f.Close() }()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)

	var schema fileSchema

	err = dec.Decode(&schema)
	if err != nil {
		return Policy{}, fmt.Errorf("%w: parse %s: %w", ErrInvalidPolicy, path, err)
	}

	policy, err := schemaToPolicy(schema)
	if err != nil {
		return Policy{}, fmt.Errorf("%w: %s: %w", ErrInvalidPolicy, path, err)
	}

	err = policy.Validate()
	if err != nil {
		return Policy{}, fmt.Errorf("%w: %s: %w", ErrInvalidPolicy, path, err)
	}

	return policy, nil
}

// schemaToPolicy lifts the YAML wire shape into the public Policy
// type. The two-step path (file → schema → Policy) keeps the YAML
// parsing isolated from the runtime auth shape; users embedding
// httpauth without YAML never pull yaml.v3 into their build.
func schemaToPolicy(s fileSchema) (Policy, error) {
	tokens := make([]TokenIdentity, 0, len(s.Tokens))

	for i, t := range s.Tokens {
		scopes, err := parseScopes(t.Scopes)
		if err != nil {
			return Policy{}, fmt.Errorf("tokens[%d] (%q): %w", i, t.ID, err)
		}

		tokens = append(tokens, TokenIdentity{
			ID:     t.ID,
			Token:  t.Token,
			Scopes: scopes,
		})
	}

	certs := make([]CertIdentity, 0, len(s.CertIdentities))

	for i, c := range s.CertIdentities {
		scopes, err := parseScopes(c.Scopes)
		if err != nil {
			return Policy{}, fmt.Errorf("cert_identities[%d] (%q): %w", i, c.SubjectCN, err)
		}

		certs = append(certs, CertIdentity{
			SubjectCN: c.SubjectCN,
			Scopes:    scopes,
		})
	}

	return Policy{
		Tokens:         tokens,
		CertIdentities: certs,
		AllowAnonymous: s.AllowAnonymous,
	}, nil
}

// parseScopes converts the wire string scope names into typed Scope
// values. Unknown scope names error with the offending name (safe
// to log — scope names are public taxonomy, not secrets) so a typo
// in the YAML fails loudly rather than silently dropping the grant.
func parseScopes(raw []string) ([]Scope, error) {
	if len(raw) == 0 {
		return nil, errEmptyScopes
	}

	out := make([]Scope, 0, len(raw))

	for _, name := range raw {
		switch Scope(name) {
		case ScopeRead, ScopeWrite, ScopeAdmin:
			out = append(out, Scope(name))
		default:
			return nil, fmt.Errorf("%w: %q", errUnknownScope, name)
		}
	}

	return out, nil
}
