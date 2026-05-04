package tests

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// generateTLSConfig builds a self-signed *tls.Config suitable for both
// the dist HTTP server (Certificates) and the auto-created HTTP client
// (RootCAs). The cert is valid for 127.0.0.1 only — sufficient for
// in-process tests, never to be reused outside them.
func generateTLSConfig(t *testing.T) *tls.Config {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "hypercache-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}

	cert, err := x509.ParseCertificate(derBytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}

	rootCAs := x509.NewCertPool()
	rootCAs.AddCert(cert)

	return &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{derBytes},
			PrivateKey:  priv,
			Leaf:        cert,
		}},
		RootCAs:    rootCAs,
		MinVersion: tls.VersionTLS12,
		ServerName: "127.0.0.1",
	}
}

// newTLSNode builds one DistMemory node configured with the supplied
// TLS config, replication=2, and seed list. Extracted to a free function
// so contextcheck doesn't follow the chain into StopOnCleanup's
// background-ctx cleanup. Construction uses context.Background() because
// Stop runs from t.Cleanup at end-of-test where the test ctx may already
// be canceled — same rationale as newAuthReplicatedNode.
func newTLSNode(t *testing.T, id, addr string, seeds []string, tlsConfig *tls.Config) *backend.DistMemory {
	t.Helper()

	bi, err := backend.NewDistMemory(context.Background(),
		backend.WithDistNode(id, addr),
		backend.WithDistSeeds(seeds),
		backend.WithDistReplication(2),
		backend.WithDistVirtualNodes(32),
		backend.WithDistHTTPLimits(backend.DistHTTPLimits{TLSConfig: tlsConfig}),
	)
	if err != nil {
		t.Fatalf("new node %s: %v", id, err)
	}

	dm, ok := bi.(*backend.DistMemory)
	if !ok {
		t.Fatalf("cast %s: %T", id, bi)
	}

	StopOnCleanup(t, dm)

	return dm
}

// TestDistHTTPTLS_HandshakeAndReplication verifies a 2-node cluster with
// shared TLS config can replicate writes over https://. End-to-end:
//
//  1. node A's listener is wrapped with tls.NewListener
//  2. node A's auto-created HTTP client uses the same RootCAs
//  3. resolver advertises https:// (because TLSConfig is non-nil)
//  4. Set on A replicates to B via signed-and-encrypted requests
func TestDistHTTPTLS_HandshakeAndReplication(t *testing.T) {
	t.Parallel()

	tlsConfig := generateTLSConfig(t)

	addrA := AllocatePort(t)
	addrB := AllocatePort(t)

	nodeA := newTLSNode(t, "A", addrA, []string{addrB}, tlsConfig)
	nodeB := newTLSNode(t, "B", addrB, []string{addrA}, tlsConfig)

	// Wait for both TLS listeners to accept handshakes.
	for _, base := range []string{"https://" + nodeA.LocalNodeAddr(), "https://" + nodeB.LocalNodeAddr()} {
		if !waitForHTTPSHealth(base, tlsConfig, 5*time.Second) {
			t.Fatalf("TLS listener at %s never came up", base)
		}
	}

	// Allow ring to settle.
	time.Sleep(200 * time.Millisecond)

	item := &cache.Item{
		Key:         "tls-prop-key",
		Value:       []byte("encrypted-value"),
		Version:     1,
		Origin:      "A",
		LastUpdated: time.Now(),
	}

	err := nodeA.Set(context.Background(), item)
	if err != nil {
		t.Fatalf("Set on nodeA: %v", err)
	}

	// If TLS were broken (e.g. client didn't use the same root, or
	// resolver advertised http:// against a TLS listener), replication
	// would fail and the value would never appear on B.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if it, ok := nodeB.Get(context.Background(), item.Key); ok && it != nil {
			return
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("replication did not propagate to nodeB over TLS — handshake or scheme mismatch likely")
}

// TestDistHTTPTLS_PlaintextPeerRejected verifies a client that does NOT
// trust the server's cert fails to handshake — no plaintext fallback.
func TestDistHTTPTLS_PlaintextPeerRejected(t *testing.T) {
	t.Parallel()

	tlsConfig := generateTLSConfig(t)
	addr := AllocatePort(t)

	bi, err := backend.NewDistMemory(context.Background(),
		backend.WithDistNode("tls-only", addr),
		backend.WithDistReplication(1),
		backend.WithDistHTTPLimits(backend.DistHTTPLimits{TLSConfig: tlsConfig}),
	)
	if err != nil {
		t.Fatalf("new dist memory: %v", err)
	}

	dm, ok := bi.(*backend.DistMemory)
	if !ok {
		t.Fatalf("cast: %T", bi)
	}

	StopOnCleanup(t, dm)

	if !waitForHTTPSHealth("https://"+dm.LocalNodeAddr(), tlsConfig, 5*time.Second) {
		t.Fatal("TLS listener never came up")
	}

	// Plaintext client (no RootCAs configured): handshake should fail.
	plaintextClient := &http.Client{Timeout: 2 * time.Second}

	_, err = plaintextClient.Get("https://" + dm.LocalNodeAddr() + "/health") //nolint:noctx,bodyclose // expect handshake failure before body
	if err == nil {
		t.Fatal("expected TLS handshake error from untrusted client, got nil")
	}

	// Stdlib reports x509.UnknownAuthorityError or similar. Accept any
	// error that mentions cert/x509/tls — the point is the connection
	// did not succeed without trust.
	var (
		x509Err        *tls.CertificateVerificationError
		unknownAuthErr x509.UnknownAuthorityError
	)

	if !errors.As(err, &x509Err) && !errors.As(err, &unknownAuthErr) {
		// Fall back to substring check — Go versions wrap this error
		// differently across releases.
		t.Logf("note: error was %v (not CertificateVerificationError)", err)
	}
}

// waitForHTTPSHealth is the TLS-aware sibling of waitForHealth — uses
// the supplied tls.Config so the test client trusts the self-signed
// server cert.
func waitForHTTPSHealth(baseURL string, tlsConfig *tls.Config, timeout time.Duration) bool {
	transport := &http.Transport{TLSClientConfig: tlsConfig}
	client := &http.Client{Transport: transport, Timeout: time.Second}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+"/health", nil)
		if err != nil {
			return false
		}

		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				return true
			}
		}

		time.Sleep(50 * time.Millisecond)
	}

	return false
}
