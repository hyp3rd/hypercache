package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTestCertPair generates a self-signed cert + key and writes
// them to tempfile paths. Returned paths are what the binary's TLS
// env vars would point at in production. Cert is valid for 127.0.0.1
// only — sufficient for the in-process buildAPITLSConfig path,
// never to be reused outside tests. Returns (certPath, keyPath) in
// that order; caught between paralleltest's preference for named
// returns and nonamedreturns' preference against them, the
// docstring is the disambiguator.
//
//nolint:revive // unnamed results: see docstring; nonamedreturns disagrees with confusing-results here
func writeTestCertPair(t *testing.T) (string, string) {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "hypercache-mtls-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	err = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes}), 0o600)
	if err != nil {
		t.Fatalf("write cert: %v", err)
	}

	err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
	if err != nil {
		t.Fatalf("write key: %v", err)
	}

	return certPath, keyPath
}

// TestBuildAPITLSConfig_Success walks the env shapes that
// buildAPITLSConfig must accept: nothing set (plaintext), cert+key
// (standard TLS), cert+key+CA (mTLS).
func TestBuildAPITLSConfig_Success(t *testing.T) {
	t.Parallel()

	certPath, keyPath := writeTestCertPair(t)
	caPath := certPath // self-sign — same cert acts as its own CA

	t.Run("neither set → nil (plaintext)", func(t *testing.T) {
		t.Parallel()
		assertPlaintextConfig(t, envConfig{})
	})

	t.Run("cert + key → standard TLS, no client auth", func(t *testing.T) {
		t.Parallel()
		assertStandardTLSConfig(t, envConfig{APITLSCert: certPath, APITLSKey: keyPath})
	})

	t.Run("cert + key + CA → mTLS with RequireAndVerifyClientCert", func(t *testing.T) {
		t.Parallel()
		assertMTLSConfig(t, envConfig{APITLSCert: certPath, APITLSKey: keyPath, APITLSCA: caPath})
	})
}

// assertPlaintextConfig verifies buildAPITLSConfig returns
// (nil, nil) — the plaintext sentinel — for the given config.
func assertPlaintextConfig(t *testing.T, cfg envConfig) {
	t.Helper()

	got, err := buildAPITLSConfig(cfg)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}

	if got != nil {
		t.Fatalf("got = %+v, want nil", got)
	}
}

// assertStandardTLSConfig verifies the returned *tls.Config is the
// standard TLS shape: cert chain populated, ClientAuth disabled
// (no CA → no client cert verification), TLS 1.2 floor.
func assertStandardTLSConfig(t *testing.T, cfg envConfig) {
	t.Helper()

	got, err := buildAPITLSConfig(cfg)
	if err != nil {
		t.Fatalf("err = %v", err)
	}

	if got == nil {
		t.Fatalf("got nil, want *tls.Config")
	}

	if got.ClientAuth != tls.NoClientCert {
		t.Errorf("ClientAuth = %v, want NoClientCert (no CA was configured)", got.ClientAuth)
	}

	if got.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %d, want %d (TLS 1.2)", got.MinVersion, tls.VersionTLS12)
	}
}

// assertMTLSConfig verifies the returned *tls.Config is the mTLS
// shape: ClientAuth=RequireAndVerifyClientCert and ClientCAs
// populated.
func assertMTLSConfig(t *testing.T, cfg envConfig) {
	t.Helper()

	got, err := buildAPITLSConfig(cfg)
	if err != nil {
		t.Fatalf("err = %v", err)
	}

	if got.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Errorf("ClientAuth = %v, want RequireAndVerifyClientCert", got.ClientAuth)
	}

	if got.ClientCAs == nil {
		t.Error("ClientCAs is nil; CA bundle was not loaded")
	}
}

// TestBuildAPITLSConfig_Errors walks the misconfiguration shapes
// that must surface as startup errors rather than silently
// degrading to plaintext or empty mTLS pools.
func TestBuildAPITLSConfig_Errors(t *testing.T) {
	t.Parallel()

	certPath, keyPath := writeTestCertPair(t)

	dir := t.TempDir()
	emptyCA := filepath.Join(dir, "empty.pem")

	err := os.WriteFile(emptyCA, []byte("not a pem"), 0o600)
	if err != nil {
		t.Fatalf("write empty: %v", err)
	}

	cases := []struct {
		name string
		cfg  envConfig
	}{
		{"cert without key", envConfig{APITLSCert: certPath}},
		{"key without cert", envConfig{APITLSKey: keyPath}},
		{"missing cert file", envConfig{APITLSCert: "/missing.pem", APITLSKey: keyPath}},
		{"missing CA file", envConfig{APITLSCert: certPath, APITLSKey: keyPath, APITLSCA: "/missing.pem"}},
		{"non-PEM CA bundle", envConfig{APITLSCert: certPath, APITLSKey: keyPath, APITLSCA: emptyCA}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := buildAPITLSConfig(tc.cfg)
			if err == nil {
				t.Fatalf("expected an error for %q config", tc.name)
			}
		})
	}
}

// TestBuildAPITLSConfig_PartialErrIsSentinel pins the error chain
// for the cert-without-key (and key-without-cert) misconfiguration
// — callers using errors.Is must be able to detect it.
func TestBuildAPITLSConfig_PartialErrIsSentinel(t *testing.T) {
	t.Parallel()

	certPath, keyPath := writeTestCertPair(t)

	_, err := buildAPITLSConfig(envConfig{APITLSCert: certPath})
	if !errors.Is(err, errAPITLSPartial) {
		t.Errorf("cert-without-key err = %v, want errors.Is(_, errAPITLSPartial)", err)
	}

	_, err = buildAPITLSConfig(envConfig{APITLSKey: keyPath})
	if !errors.Is(err, errAPITLSPartial) {
		t.Errorf("key-without-cert err = %v, want errors.Is(_, errAPITLSPartial)", err)
	}
}
