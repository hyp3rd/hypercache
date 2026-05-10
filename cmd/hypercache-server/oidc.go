package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	fiber "github.com/gofiber/fiber/v3"
	"github.com/hyp3rd/ewrap"

	"github.com/hyp3rd/hypercache/pkg/httpauth"
)

// oidcVerifierOptions captures the runtime knobs the OIDC verifier
// closure needs. Built once at boot from envConfig and embedded in
// the closure so handler invocations don't touch process state.
type oidcVerifierOptions struct {
	// IDTokenVerifier is go-oidc's pre-configured signature/claim
	// validator: it knows the issuer's JWKS, the expected audience,
	// and how to validate the standard timing claims (exp/iat/nbf).
	IDTokenVerifier *oidc.IDTokenVerifier
	IdentityClaim   string // "sub" (default) or "email"
	ScopeClaim      string // "scope" (default — space-separated string) or a custom array claim
}

// Sentinel errors returned by the OIDC verifier closure. err113
// flags `fmt.Errorf("oidc: ...")` calls without a sentinel as
// dynamic-error code smells; static sentinels wrapped via `%w`
// satisfy the lint and let downstream callers `errors.Is` against
// each failure mode.
var (
	// errOIDCMissingBearer is returned when the request carries no
	// Authorization: Bearer header. The resolve chain in Policy
	// treats any error from ServerVerify as "this verifier
	// declined" and continues to the AllowAnonymous fallback (or
	// 401 if none configured).
	errOIDCMissingBearer = ewrap.New("oidc: no bearer token in request")
	// errOIDCIdentityMissing — the configured identity claim is
	// absent from the JWT.
	errOIDCIdentityMissing = ewrap.New("oidc: identity claim missing from JWT")
	// errOIDCIdentityWrongType — the identity claim is present
	// but not a string.
	errOIDCIdentityWrongType = ewrap.New("oidc: identity claim is wrong type")
	// errOIDCIdentityEmpty — the identity claim is a string but
	// empty; httpauth.Identity.ID would be useless.
	errOIDCIdentityEmpty = ewrap.New("oidc: identity claim is empty")
)

// buildOIDCVerifier constructs an OIDC IdToken verifier and returns
// it wrapped in the Policy.ServerVerify shape:
// `func(fiber.Ctx) (httpauth.Identity, error)`. The IdP discovery
// (`/.well-known/openid-configuration`) happens here and may block
// briefly on first call; failure aborts boot so the operator sees
// a misconfigured IdP at startup rather than silent 401s in
// production.
//
// Why a closure rather than a struct method: Policy.ServerVerify
// is `func(fiber.Ctx) (Identity, error)` by contract; a closure
// captures the pre-built verifier without exporting a type the
// rest of the binary doesn't need.
//
// Scope mapping: only the three known scope strings ("read",
// "write", "admin") survive. Unknown strings in the JWT's scope
// claim are dropped silently — extending the cache's scope set is
// a coordinated change in pkg/httpauth/policy.go, and an unknown
// scope at runtime can't unlock anything because the proxy's
// per-route check is also strict.
func buildOIDCVerifier(ctx context.Context, cfg envConfig) (func(fiber.Ctx) (httpauth.Identity, error), error) {
	provider, err := oidc.NewProvider(ctx, cfg.OIDCIssuer)
	if err != nil {
		return nil, fmt.Errorf("oidc: discovery for issuer %q failed: %w", cfg.OIDCIssuer, err)
	}

	verifier := provider.Verifier(&oidc.Config{
		ClientID: cfg.OIDCAudience,
	})

	opts := oidcVerifierOptions{
		IDTokenVerifier: verifier,
		IdentityClaim:   cfg.OIDCIdentityClaim,
		ScopeClaim:      cfg.OIDCScopeClaim,
	}

	return makeOIDCServerVerify(opts), nil
}

// makeOIDCServerVerify is the closure factory split out from the
// boot-time construction so tests can drive the verifier with a
// stub IDTokenVerifier (configured against an in-process IdP) and
// avoid the network discovery dance.
//
// Verifier work that takes a context.Context lives in
// verifyAndExtract below. The closure itself is just an adapter
// from fiber.Ctx → context.Context — keeps contextcheck happy
// (typed helpers receive ctx as a parameter; the closure's only
// "context" comes from the framework boundary).
func makeOIDCServerVerify(opts oidcVerifierOptions) func(fiber.Ctx) (httpauth.Identity, error) {
	return func(c fiber.Ctx) (httpauth.Identity, error) {
		raw, ok := bearerFromHeader(c.Get("Authorization"))
		if !ok {
			return httpauth.Identity{}, errOIDCMissingBearer
		}

		return verifyAndExtract(c.Context(), opts, raw)
	}
}

// verifyAndExtract validates the given raw JWT against opts and
// projects the configured claims onto httpauth.Identity. Receives
// ctx as a first parameter so the per-request cancellation flows
// into the IDTokenVerifier (go-oidc honors ctx during JWKS lookups
// triggered by key rotation).
func verifyAndExtract(
	ctx context.Context,
	opts oidcVerifierOptions,
	raw string,
) (httpauth.Identity, error) {
	idToken, err := opts.IDTokenVerifier.Verify(ctx, raw)
	if err != nil {
		return httpauth.Identity{}, fmt.Errorf("oidc: verify: %w", err)
	}

	var claims map[string]any

	err = idToken.Claims(&claims)
	if err != nil {
		return httpauth.Identity{}, fmt.Errorf("oidc: claim decode: %w", err)
	}

	identityID, err := extractIdentityClaim(claims, opts.IdentityClaim)
	if err != nil {
		return httpauth.Identity{}, err
	}

	scopes := extractScopeClaim(claims, opts.ScopeClaim)

	return httpauth.Identity{ID: identityID, Scopes: scopes}, nil
}

// bearerFromHeader returns the token portion of an
// `Authorization: Bearer <token>` header. Returns ("", false) on
// any other shape — including empty headers and bare-token
// (no "Bearer " prefix) — leaving the chain to fall through to
// AllowAnonymous or 401.
func bearerFromHeader(header string) (string, bool) {
	const prefix = "Bearer "

	if !strings.HasPrefix(header, prefix) {
		return "", false
	}

	token := header[len(prefix):]
	if token == "" {
		return "", false
	}

	return token, true
}

// extractIdentityClaim reads the configured identity claim
// (default "sub") from the JWT claims map. The claim must be a
// non-empty string; numeric or array values are rejected because
// httpauth.Identity.ID is a human-readable label and a numeric
// or array shape would mask the operator's true identity in
// audit logs.
//
// Returns wrapped sentinel errors (errOIDCIdentity*) so callers
// can `errors.Is` against each failure mode and so err113 stays
// happy — dynamic format strings here would conflate distinct
// failure modes under one untyped fmt.Errorf string.
func extractIdentityClaim(claims map[string]any, claim string) (string, error) {
	raw, ok := claims[claim]
	if !ok {
		return "", fmt.Errorf("%w: %q", errOIDCIdentityMissing, claim)
	}

	id, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%w: claim %q is %T, want string", errOIDCIdentityWrongType, claim, raw)
	}

	if id == "" {
		return "", fmt.Errorf("%w: claim %q", errOIDCIdentityEmpty, claim)
	}

	return id, nil
}

// extractScopeClaim reads the configured scope claim from the JWT
// and projects it onto the three known httpauth.Scope values.
// Both shapes are supported:
//
//   - Standard OAuth2 "scope": space-separated string
//     ("read write admin")
//   - Custom array claim: JSON array of strings
//     (["read", "write", "admin"])
//
// Unknown scope strings are dropped silently. Missing / wrong-
// type claims yield an empty slice — equivalent to "no scopes"
// — so the request authenticates but cannot reach scoped routes.
// This is deliberate: a malformed scope claim should not leak
// access to scoped endpoints, but it should also not block
// liveness / openapi-spec endpoints that need no scope.
func extractScopeClaim(claims map[string]any, claim string) []httpauth.Scope {
	raw, ok := claims[claim]
	if !ok {
		return nil
	}

	var tokens []string

	switch v := raw.(type) {
	case string:
		tokens = strings.Fields(v) // space-separated
	case []any:
		tokens = make([]string, 0, len(v))

		for _, item := range v {
			s, isString := item.(string)
			if isString {
				tokens = append(tokens, s)
			}
		}

	default:
		// Unknown shape — treat as empty.
		return nil
	}

	out := make([]httpauth.Scope, 0, len(tokens))

	for _, t := range tokens {
		switch t {
		case string(httpauth.ScopeRead):
			out = append(out, httpauth.ScopeRead)
		case string(httpauth.ScopeWrite):
			out = append(out, httpauth.ScopeWrite)
		case string(httpauth.ScopeAdmin):
			out = append(out, httpauth.ScopeAdmin)
		default:
			// Unknown scope — ignore. The proxy's per-route
			// check is strict, so an unrecognized scope can't
			// unlock anything regardless.
		}
	}

	return out
}
