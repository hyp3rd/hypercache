package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	fiber "github.com/gofiber/fiber/v3"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/pkg/backend"
	"github.com/hyp3rd/hypercache/pkg/httpauth"
)

// signedCert is one cert + matching private key, plus the DER bytes
// the CA pool needs to verify it. Returned by issueCert below so the
// E2E test can wire the same data into both PEM files (the binary
// reads them off disk) and the http.Client (the request side).
type signedCert struct {
	cert    *x509.Certificate
	der     []byte
	key     *ecdsa.PrivateKey
	pemCert []byte
	pemKey  []byte
}

// issueCert produces a self-signed CA-or-leaf cert. When signer is
// nil the resulting cert self-signs (used for the CA root). When
// signer is non-nil the CA signs the leaf — that's how the client
// and server certs get a verifiable chain back to the test CA.
func issueCert(t *testing.T, cn string, isCA bool, signer *signedCert, sans []string) *signedCert {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key for %s: %v", cn, err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}

	if isCA {
		template.IsCA = true

		template.KeyUsage |= x509.KeyUsageCertSign

		template.BasicConstraintsValid = true
	}

	for _, san := range sans {
		if ip := net.ParseIP(san); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, san)
		}
	}

	parent := template
	parentKey := any(key)

	if signer != nil {
		parent = signer.cert
		parentKey = signer.key
	}

	der, err := x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatalf("create cert %s: %v", cn, err)
	}

	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert %s: %v", cn, err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	return &signedCert{
		cert:    parsed,
		der:     der,
		key:     key,
		pemCert: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pemKey:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	}
}

// writePEM persists pem-encoded bytes to a tempfile and returns the
// path; sole purpose is feeding the binary's env-var-driven file
// paths.
func writePEM(t *testing.T, dir, name string, data []byte) string {
	t.Helper()

	path := filepath.Join(dir, name)

	err := os.WriteFile(path, data, 0o600)
	if err != nil {
		t.Fatalf("write %s: %v", path, err)
	}

	return path
}

// TestMTLS_E2E_ClientCertResolvesIdentity is the integration test
// that proves the full mTLS path is wired correctly: a real TLS
// handshake against the configured envConfig, with a client
// presenting a cert whose Subject CN matches a configured
// CertIdentity. The auth middleware must resolve the cert into
// an Identity with the right scopes and let the request through
// to the handler.
//
// Without this test, every individual unit test passes but the
// composition (env-vars → tls.Config → fiber listener →
// Policy.resolveCert) could be silently broken.
//
// Cannot t.Parallel() — binds to a fresh ephemeral port and
// shares the test-process server until t.Cleanup; sequential
// execution avoids any port-reuse / goroutine-leak surprises in
// fiber's listener teardown.
//
//nolint:paralleltest // intentional: real listener owned by this test
func TestMTLS_E2E_ClientCertResolvesIdentity(t *testing.T) {
	if testing.Short() {
		t.Skip("E2E mTLS test starts a real listener; skip with -short")
	}

	dir := t.TempDir()

	ca := issueCert(t, "test-ca", true, nil, nil)
	server := issueCert(t, "test-server", false, ca, []string{"127.0.0.1"})
	client := issueCert(t, "test-client", false, ca, nil)

	caPath := writePEM(t, dir, "ca.pem", ca.pemCert)
	serverCertPath := writePEM(t, dir, "server.crt", server.pemCert)
	serverKeyPath := writePEM(t, dir, "server.key", server.pemKey)

	hc := newE2ECacheNode(t)

	cfg := envConfig{
		APIAddr:    "127.0.0.1:0", // ephemeral port; real bind below
		NodeID:     "mtls-test-node",
		APITLSCert: serverCertPath,
		APITLSKey:  serverKeyPath,
		APITLSCA:   caPath,
		AuthPolicy: httpauth.Policy{
			CertIdentities: []httpauth.CertIdentity{
				{SubjectCN: "test-client", Scopes: []httpauth.Scope{httpauth.ScopeRead}},
			},
		},
	}

	addr, app := startTLSServer(t, cfg, hc)

	clientTLS := buildClientTLS(t, ca.cert, client.der, client.key)

	target := "https://" + addr + "/v1/owners/k"

	//nolint:paralleltest // subtests share the parent's listener
	t.Run("client cert with matching CN → 200", func(_ *testing.T) {
		status := doMTLSRequest(t, target, clientTLS)
		if status != http.StatusOK {
			t.Fatalf("got status %d, want 200", status)
		}
	})

	//nolint:paralleltest // subtests share the parent's listener
	t.Run("client cert without matching CN → 401", func(_ *testing.T) {
		// Issue a fresh client cert with a CN that is NOT in
		// CertIdentities; the policy should reject it (cert is
		// validly signed by the CA, but the CN does not map to
		// any configured identity).
		stranger := issueCert(t, "stranger", false, ca, nil)
		strangerTLS := buildClientTLS(t, ca.cert, stranger.der, stranger.key)

		status := doMTLSRequest(t, target, strangerTLS)
		if status != http.StatusUnauthorized {
			t.Fatalf("got status %d, want 401", status)
		}
	})

	t.Cleanup(func() { _ = app.ShutdownWithContext(context.Background()) })
}

// newE2ECacheNode spins up a single-replica DistMemory hypercache
// for the E2E test, returns it bound to t.Cleanup. Replication=1
// avoids the wait-for-quorum dance — we only care about the auth
// middleware, not the cache semantics.
func newE2ECacheNode(t *testing.T) *hypercache.HyperCache[backend.DistMemory] {
	t.Helper()

	cfg, err := hypercache.NewConfig[backend.DistMemory](constants.DistMemoryBackend)
	if err != nil {
		t.Fatalf("new config: %v", err)
	}

	cfg.DistMemoryOptions = []backend.DistMemoryOption{
		backend.WithDistNode("mtls-test-node", "127.0.0.1:0"),
		backend.WithDistReplication(1),
	}

	hc, err := hypercache.New(t.Context(), hypercache.GetDefaultManager(), cfg)
	if err != nil {
		t.Fatalf("new hypercache: %v", err)
	}

	t.Cleanup(func() { _ = hc.Stop(context.Background()) })

	return hc
}

// startTLSServer constructs the TLS config and binds to a real
// 127.0.0.1 port, hands the listener to fiber, and returns the
// resolved address. We bind ourselves rather than going through
// runClientAPI because runClientAPI binds inside a goroutine and
// the test needs the resolved port up-front.
func startTLSServer(t *testing.T, cfg envConfig, hc *hypercache.HyperCache[backend.DistMemory]) (string, *fiber.App) {
	t.Helper()

	tlsCfg, err := buildAPITLSConfig(cfg)
	if err != nil {
		t.Fatalf("build TLS config: %v", err)
	}

	if tlsCfg == nil {
		t.Fatalf("expected non-nil TLS config")
	}

	ln, err := tls.Listen("tcp", cfg.APIAddr, tlsCfg)
	if err != nil {
		t.Fatalf("tls listen: %v", err)
	}

	app := fiber.New()
	registerClientRoutes(app, cfg.AuthPolicy, &nodeContext{hc: hc, nodeID: cfg.NodeID})

	go func() {
		err := app.Listener(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Logf("listener exited: %v", err)
		}
	}()

	// Wait briefly for fiber to start serving — without this the
	// first request races the listener init and gets ECONNREFUSED.
	// Use Dialer.DialContext rather than tls.Dial so the readiness
	// probe inherits the test ctx (timeout/cancel propagate cleanly).
	addr := ln.Addr().String()
	awaitTLSReady(t, addr)

	return addr, app
}

// awaitTLSReady polls the listener until it accepts a TLS dial,
// or the deadline expires. The skip-verify is intentional: this
// is a startup-readiness probe, not a real client. Cert validation
// happens on the actual test request a few lines later.
func awaitTLSReady(t *testing.T, addr string) {
	t.Helper()

	dialer := &tls.Dialer{
		Config: &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // dev-mode readiness probe; not a trust boundary
			MinVersion:         tls.VersionTLS12,
		},
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, dialErr := dialer.DialContext(t.Context(), "tcp", addr)
		if dialErr == nil {
			_ = conn.Close()

			return
		}

		time.Sleep(20 * time.Millisecond)
	}
}

// buildClientTLS assembles the per-test http.Client TLS config:
// trust the test CA, present the test client's cert.
func buildClientTLS(t *testing.T, caCert *x509.Certificate, clientDER []byte, clientKey *ecdsa.PrivateKey) *tls.Config {
	t.Helper()

	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	return &tls.Config{
		RootCAs: pool,
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{clientDER},
			PrivateKey:  clientKey,
		}},
		MinVersion: tls.VersionTLS12,
		ServerName: "127.0.0.1",
	}
}

// doMTLSRequest issues a GET against url with the supplied client
// TLS config and returns the status code. Body is drained and
// discarded — we only assert auth resolution, not handler logic.
func doMTLSRequest(t *testing.T, target string, tlsCfg *tls.Config) int {
	t.Helper()

	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}

	c := &http.Client{
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
		Timeout:   3 * time.Second,
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, parsed.String(), http.NoBody)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	_, _ = io.Copy(io.Discard, resp.Body)

	return resp.StatusCode
}
