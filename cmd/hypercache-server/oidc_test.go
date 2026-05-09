package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	fiber "github.com/gofiber/fiber/v3"

	"github.com/hyp3rd/hypercache/pkg/httpauth"
)

// Test-fixture constants. Hoisted to satisfy goconst — these
// strings repeat across every table-driven test in this file with
// no semantic significance per occurrence.
const (
	testAudience     = "hypercache-test"
	claimSub         = "sub"
	claimAud         = "aud"
	claimScope       = "scope"
	claimEmail       = "email"
	scopeRead        = "read"
	scopeWrite       = "write"
	scopeAdmin       = "admin"
	identityOps      = "ops"
	identityOperator = "operator@example.com"
)

// idpStub is an in-process OIDC IdP just rich enough for go-oidc's
// verifier to validate JWTs. Two endpoints:
//
//   - /.well-known/openid-configuration: the discovery doc
//   - /jwks: the public-key set
//
// No /authorize, no /token — those are only needed for the OAuth
// dance, not for IdToken verification (which validates a JWT
// already in hand).
type idpStub struct {
	server *httptest.Server
	signer jose.Signer
	issuer string
	keyID  string
}

// newIdPStub mints an RSA key pair, builds a JWKS document from
// the public key, and starts a tiny HTTP server that serves it.
// The caller derives JWTs via stub.sign() and points the OIDC
// verifier at stub.issuer().
func newIdPStub(t *testing.T) *idpStub {
	t.Helper()

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	const keyID = "test-key-1"

	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: priv},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader(jose.HeaderKey("kid"), keyID),
	)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}

	jwks := jose.JSONWebKeySet{
		Keys: []jose.JSONWebKey{
			{
				Key:       &priv.PublicKey,
				KeyID:     keyID,
				Algorithm: "RS256",
				Use:       "sig",
			},
		},
	}

	stub := &idpStub{signer: signer, keyID: keyID}

	mux := http.NewServeMux()

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                stub.issuer,
			"jwks_uri":                              stub.issuer + "/jwks",
			"authorization_endpoint":                stub.issuer + "/authorize",
			"token_endpoint":                        stub.issuer + "/token",
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
		})
	})

	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		_ = json.NewEncoder(w).Encode(jwks)
	})

	stub.server = httptest.NewServer(mux)
	stub.issuer = stub.server.URL

	t.Cleanup(stub.server.Close)

	return stub
}

// sign builds a signed JWT with the given claim map. Always
// stamps `iss` (matches the stub's URL) and the standard timing
// claims unless caller overrides them.
func (s *idpStub) sign(t *testing.T, claims map[string]any) string {
	t.Helper()

	merged := map[string]any{
		"iss": s.issuer,
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
	}
	maps.Copy(merged, claims)

	token, err := jwt.Signed(s.signer).Claims(merged).Serialize()
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}

	return token
}

// newVerifier builds a real `*oidc.IDTokenVerifier` against the
// stub with `testAudience` as the expected client ID. Hoisting
// the audience to a package constant keeps unparam happy (no
// always-same parameter); tests that need a different audience
// build the verifier inline.
func (s *idpStub) newVerifier(t *testing.T) *oidc.IDTokenVerifier {
	t.Helper()

	provider, err := oidc.NewProvider(t.Context(), s.issuer)
	if err != nil {
		t.Fatalf("oidc discovery: %v", err)
	}

	return provider.Verifier(&oidc.Config{ClientID: testAudience})
}

// driveVerify constructs a fiber app that runs `verify` on
// `/probe` and returns the resulting status + Identity. Lets the
// test body assert just the post-verify state without rebuilding
// a fiber stack inline.
func driveVerify(
	t *testing.T,
	verify func(fiber.Ctx) (httpauth.Identity, error),
	authHeader string,
) (httpauth.Identity, error) {
	t.Helper()

	app := fiber.New()

	var (
		got      httpauth.Identity
		probeErr error
	)

	app.Get("/probe", func(c fiber.Ctx) error {
		id, err := verify(c)

		got, probeErr = id, err

		return c.SendStatus(http.StatusOK)
	})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/probe", http.NoBody)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}

	_ = resp.Body.Close()

	return got, probeErr
}

func TestOIDC_VerifyValidToken(t *testing.T) {
	t.Parallel()

	stub := newIdPStub(t)

	const audience = "hypercache-test"

	verifier := makeOIDCServerVerify(oidcVerifierOptions{
		IDTokenVerifier: stub.newVerifier(t),
		IdentityClaim:   claimSub,
		ScopeClaim:      claimScope,
	})

	token := stub.sign(t, map[string]any{
		claimAud:   audience,
		claimSub:   identityOperator,
		claimScope: scopeRead + " " + scopeWrite,
	})

	id, err := driveVerify(t, verifier, "Bearer "+token)
	if err != nil {
		t.Fatalf("verify failed: %v", err)
	}

	if id.ID != "operator@example.com" {
		t.Errorf("identity ID: got %q, want operator@example.com", id.ID)
	}

	if len(id.Scopes) != 2 || id.Scopes[0] != httpauth.ScopeRead || id.Scopes[1] != httpauth.ScopeWrite {
		t.Errorf("scopes: got %v, want [read write]", id.Scopes)
	}
}

func TestOIDC_RejectsExpiredToken(t *testing.T) {
	t.Parallel()

	stub := newIdPStub(t)

	const audience = "hypercache-test"

	verifier := makeOIDCServerVerify(oidcVerifierOptions{
		IDTokenVerifier: stub.newVerifier(t),
		IdentityClaim:   claimSub,
		ScopeClaim:      claimScope,
	})

	token := stub.sign(t, map[string]any{
		claimAud:   audience,
		claimSub:   identityOps,
		claimScope: scopeRead,
		"exp":      time.Now().Add(-time.Hour).Unix(), // expired
	})

	_, err := driveVerify(t, verifier, "Bearer "+token)
	if err == nil {
		t.Fatal("expected error for expired token, got nil")
	}
}

func TestOIDC_RejectsWrongAudience(t *testing.T) {
	t.Parallel()

	stub := newIdPStub(t)

	verifier := makeOIDCServerVerify(oidcVerifierOptions{
		IDTokenVerifier: stub.newVerifier(t),
		IdentityClaim:   claimSub,
		ScopeClaim:      claimScope,
	})

	token := stub.sign(t, map[string]any{
		"aud":      "different-service", // wrong audience
		claimSub:   identityOps,
		claimScope: scopeRead,
	})

	_, err := driveVerify(t, verifier, "Bearer "+token)
	if err == nil {
		t.Fatal("expected error for wrong audience, got nil")
	}
}

func TestOIDC_RejectsBadSignature(t *testing.T) {
	t.Parallel()

	// Two independent stubs — one signs the JWT, the verifier
	// fetches JWKS from the OTHER. The signature lookup fails
	// because the kid doesn't match any key in the verifier's
	// JWKS.
	signerStub := newIdPStub(t)
	verifierStub := newIdPStub(t)

	const audience = "hypercache-test"

	verifier := makeOIDCServerVerify(oidcVerifierOptions{
		IDTokenVerifier: verifierStub.newVerifier(t),
		IdentityClaim:   claimSub,
		ScopeClaim:      claimScope,
	})

	// Sign with signerStub but issued by verifierStub so the
	// `iss` check passes; only the signature is wrong.
	token := signerStub.sign(t, map[string]any{
		"iss":      verifierStub.issuer,
		claimAud:   audience,
		claimSub:   identityOps,
		claimScope: scopeRead,
	})

	_, err := driveVerify(t, verifier, "Bearer "+token)
	if err == nil {
		t.Fatal("expected error for bad signature, got nil")
	}
}

func TestOIDC_NoBearerHeader(t *testing.T) {
	t.Parallel()

	stub := newIdPStub(t)

	verifier := makeOIDCServerVerify(oidcVerifierOptions{
		IDTokenVerifier: stub.newVerifier(t),
		IdentityClaim:   claimSub,
		ScopeClaim:      claimScope,
	})

	// No Authorization header — must return errOIDCMissingBearer
	// so the resolve chain falls through to AllowAnonymous (or
	// 401). Returning a generic JWT-validation error here would
	// mask the empty-header case from operators reading logs.
	_, err := driveVerify(t, verifier, "")
	if !errors.Is(err, errOIDCMissingBearer) {
		t.Fatalf("got %v, want errOIDCMissingBearer", err)
	}
}

func TestOIDC_IdentityClaim_Email(t *testing.T) {
	t.Parallel()

	stub := newIdPStub(t)

	const audience = "hypercache-test"

	verifier := makeOIDCServerVerify(oidcVerifierOptions{
		IDTokenVerifier: stub.newVerifier(t),
		IdentityClaim:   claimEmail, // not the default "sub"
		ScopeClaim:      claimScope,
	})

	token := stub.sign(t, map[string]any{
		claimAud:   audience,
		claimSub:   "ignored-because-email-is-the-claim",
		claimEmail: identityOperator,
		claimScope: scopeRead,
	})

	id, err := driveVerify(t, verifier, "Bearer "+token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	if id.ID != identityOperator {
		t.Errorf("identity ID: got %q, want %s", id.ID, identityOperator)
	}
}

func TestOIDC_IdentityClaim_Missing(t *testing.T) {
	t.Parallel()

	stub := newIdPStub(t)

	const audience = "hypercache-test"

	verifier := makeOIDCServerVerify(oidcVerifierOptions{
		IDTokenVerifier: stub.newVerifier(t),
		IdentityClaim:   "preferred_username",
		ScopeClaim:      claimScope,
	})

	token := stub.sign(t, map[string]any{
		claimAud:   audience,
		claimSub:   identityOps,
		claimScope: scopeRead,
		// no preferred_username
	})

	_, err := driveVerify(t, verifier, "Bearer "+token)
	if err == nil {
		t.Fatal("expected error for missing identity claim, got nil")
	}
}

func TestOIDC_ScopeClaim_ArrayShape(t *testing.T) {
	t.Parallel()

	stub := newIdPStub(t)

	const audience = "hypercache-test"

	verifier := makeOIDCServerVerify(oidcVerifierOptions{
		IDTokenVerifier: stub.newVerifier(t),
		IdentityClaim:   claimSub,
		ScopeClaim:      "cache_scopes",
	})

	token := stub.sign(t, map[string]any{
		"aud":          audience,
		claimSub:       identityOps,
		"cache_scopes": []string{"read", "admin"},
	})

	id, err := driveVerify(t, verifier, "Bearer "+token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	if len(id.Scopes) != 2 || id.Scopes[0] != httpauth.ScopeRead || id.Scopes[1] != httpauth.ScopeAdmin {
		t.Errorf("scopes: got %v, want [read admin]", id.Scopes)
	}
}

func TestOIDC_ScopeClaim_DropsUnknownScopes(t *testing.T) {
	t.Parallel()

	stub := newIdPStub(t)

	const audience = "hypercache-test"

	verifier := makeOIDCServerVerify(oidcVerifierOptions{
		IDTokenVerifier: stub.newVerifier(t),
		IdentityClaim:   claimSub,
		ScopeClaim:      claimScope,
	})

	token := stub.sign(t, map[string]any{
		claimAud: audience,
		claimSub: identityOps,
		"scope":  "read trace openid email", // only read maps to a known scope
	})

	id, err := driveVerify(t, verifier, "Bearer "+token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	if len(id.Scopes) != 1 || id.Scopes[0] != httpauth.ScopeRead {
		t.Errorf("scopes: got %v, want [read]", id.Scopes)
	}
}

func TestOIDC_ScopeClaim_MissingYieldsEmpty(t *testing.T) {
	t.Parallel()

	stub := newIdPStub(t)

	const audience = "hypercache-test"

	verifier := makeOIDCServerVerify(oidcVerifierOptions{
		IDTokenVerifier: stub.newVerifier(t),
		IdentityClaim:   claimSub,
		ScopeClaim:      claimScope,
	})

	token := stub.sign(t, map[string]any{
		"aud":    audience,
		claimSub: identityOps,
		// no scope claim at all
	})

	id, err := driveVerify(t, verifier, "Bearer "+token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	if len(id.Scopes) != 0 {
		t.Errorf("scopes: got %v, want empty", id.Scopes)
	}

	if id.ID != "ops" {
		t.Errorf("identity ID: got %q, want ops", id.ID)
	}
}

// TestBuildOIDCVerifier_DiscoveryRPC pins the boot-time discovery
// behavior: a successful discovery returns a usable verifier; a
// bad issuer URL fails fast (rather than silently producing a
// verifier that 401s every JWT).
func TestBuildOIDCVerifier_DiscoveryRPC(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		stub := newIdPStub(t)

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		verify, err := buildOIDCVerifier(ctx, envConfig{
			OIDCIssuer:        stub.issuer,
			OIDCAudience:      "hypercache-test",
			OIDCIdentityClaim: claimSub,
			OIDCScopeClaim:    "scope",
		})
		if err != nil {
			t.Fatalf("buildOIDCVerifier: %v", err)
		}

		if verify == nil {
			t.Fatal("verifier nil on success")
		}
	})

	t.Run("unreachable issuer fails fast", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()

		_, err := buildOIDCVerifier(ctx, envConfig{
			OIDCIssuer:        "http://127.0.0.1:1", // no listener
			OIDCAudience:      "anything",
			OIDCIdentityClaim: claimSub,
			OIDCScopeClaim:    "scope",
		})
		if err == nil {
			t.Fatal("expected error for unreachable issuer")
		}
	})
}
