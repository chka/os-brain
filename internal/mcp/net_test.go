package mcp

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chka/os-brain/internal/clitest"
)

// Serving MCP over HTTP beyond loopback. Off loopback the requests cross a network, where
// a bearer token sent in plain HTTP can be read by anyone on the path, so the server
// refuses to listen without both TLS and a token.

// selfSigned writes a certificate for 127.0.0.1 and its key and returns the paths and a pool
// that trusts it.
func selfSigned(t *testing.T) (string, string, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cert, kf := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kf, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), 0o600); err != nil {
		t.Fatal(err)
	}
	parsed, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	return cert, kf, pool
}

// The endpoint serves every memory in the brain, so even with TLS in place an address
// off loopback is refused without a token and the refusal names the flag.
func TestHTTPRefusesToListenOffLoopbackWithoutAToken(t *testing.T) {
	clitest.Brain(t, "mcptest")
	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	cert, key, _ := selfSigned(t)
	err = s.serveHTTP(httpOpts{addr: "0.0.0.0:0", tlsCert: cert, tlsKey: key})
	if err == nil || !strings.Contains(err.Error(), "--token") {
		t.Errorf("expected a refusal naming --token, got %v", err)
	}
}

// Off loopback, no certificate means no server, whatever else is set: a token, or a public
// URL that suggests a TLS proxy in front, is not enough on its own. A certificate needs its
// key, TLS still needs a token, the floor is TLS 1.2 and loopback stays plain HTTP behind
// its bearer.
func TestHTTPOffLoopbackRefusesWithoutTLS(t *testing.T) {
	clitest.Brain(t, "mcptest")
	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range []httpOpts{
		{addr: "0.0.0.0:0", token: "a-long-enough-token"},
		{addr: ":0", token: "a-long-enough-token"},
		{addr: "0.0.0.0:0", token: "x", publicURL: "https://laptop.example.ts.net"},
	} {
		err := s.serveHTTP(o)
		if err == nil || !strings.Contains(err.Error(), "without TLS") {
			t.Errorf("%s with no certificate: %v", o.addr, err)
		}
	}
	cert, key, _ := selfSigned(t)
	if _, err := checkHTTPOpts(httpOpts{addr: "0.0.0.0:0", tlsCert: cert}); err == nil ||
		!strings.Contains(err.Error(), "go together") {
		t.Errorf("a certificate without its key: %v", err)
	}
	if _, err := checkHTTPOpts(httpOpts{addr: "0.0.0.0:0", tlsCert: cert, tlsKey: key}); err == nil ||
		!strings.Contains(err.Error(), "--token") {
		t.Errorf("TLS off loopback still needs a token: %v", err)
	}
	if cfg, err := checkHTTPOpts(httpOpts{addr: "0.0.0.0:0", token: "t", tlsCert: cert, tlsKey: key}); err != nil ||
		cfg == nil || cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("TLS off loopback with a token: %v %v", cfg, err)
	}
	if cfg, err := checkHTTPOpts(httpOpts{addr: "127.0.0.1:0"}); err != nil || cfg != nil {
		t.Errorf("loopback stays plain HTTP behind its bearer: %v %v", cfg, err)
	}
}

// With a certificate the server really speaks TLS: a client that trusts the certificate
// gets through, a client offering only TLS 1.1 or older is turned away and a plain HTTP
// request to the same port is not served.
func TestHTTPServesTLSWithTheGivenCertificate(t *testing.T) {
	clitest.Brain(t, "mcptest")
	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	cert, key, pool := selfSigned(t)
	o := httpOpts{addr: "127.0.0.1:0", token: "tls-token", tlsCert: cert, tlsKey: key}
	cfg, err := checkHTTPOpts(o)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: s.httpHandler(o)}
	go func() { _ = serveOn(srv, ln, cfg) }()
	defer srv.Close()
	base := "https://" + ln.Addr().String() + "/mcp"

	c := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	req, _ := http.NewRequest("POST", base, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tls-token")
	r, err := c.Do(req)
	if err != nil {
		t.Fatalf("an https request failed: %v", err)
	}
	r.Body.Close()
	if r.StatusCode != 200 || r.TLS == nil {
		t.Fatalf("https initialize: %d tls=%v", r.StatusCode, r.TLS != nil)
	}
	old := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool,
		MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11}}}
	if _, err := old.Get(base); err == nil {
		t.Error("TLS 1.1 was accepted")
	}
	if r, err := http.Post("http://"+ln.Addr().String()+"/mcp", "application/json", strings.NewReader("{}")); err == nil {
		r.Body.Close()
		if r.StatusCode == 200 {
			t.Error("plain HTTP was served on the TLS port")
		}
	}
}
