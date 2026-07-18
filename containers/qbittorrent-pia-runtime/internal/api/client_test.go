package api

import (
	"context"
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
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) *os.File {
	t.Helper()
	file, err := os.Open(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	return file
}

func TestCandidateFilteringOrderingCooldownAndBound(t *testing.T) {
	list, err := ParseServerList(fixture(t, "server-list.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	candidates := SelectCandidates(list, []string{"uk_london", "ca_ontario"}, map[string]time.Time{"192.0.2.12": now.Add(time.Hour)}, now, 2)
	if len(candidates) != 2 {
		t.Fatalf("candidate count=%d", len(candidates))
	}
	if candidates[0].RegionID != "uk_london" || candidates[1].IP != "192.0.2.11" {
		t.Fatalf("unexpected ordering: %#v", candidates)
	}
	for _, candidate := range candidates {
		if candidate.Country == "US" {
			t.Fatal("US candidate was not excluded")
		}
	}
}

func TestMalformedAndIncompleteServerLists(t *testing.T) {
	for _, value := range []string{"{", `{"regions":[]}`, `{"groups":{"wg":[{}]},"regions":[{"id":"x"}]}`} {
		if _, err := ParseServerList(strings.NewReader(value)); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}

func TestTokenParsingAndRedaction(t *testing.T) {
	const username = "fixture-user-sensitive"
	const password = "fixture-password-sensitive"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1024); err != nil {
			t.Error(err)
		}
		if r.FormValue("username") != username || r.FormValue("password") != password {
			t.Error("credentials not sent as multipart fields")
		}
		_, _ = w.Write([]byte(`{"token":"fixture-token-sensitive"}`))
	}))
	defer server.Close()
	client := &Client{TokenURL: server.URL}
	token, err := client.Token(context.Background(), username, password)
	if err != nil {
		t.Fatal(err)
	}
	if token != "fixture-token-sensitive" {
		t.Fatal("token parse failed")
	}
	failure := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("fixture-token-sensitive " + password))
	}))
	defer failure.Close()
	client.TokenURL = failure.URL
	_, err = client.Token(context.Background(), username, password)
	if err == nil {
		t.Fatal("expected failure")
	}
	for _, secret := range []string{username, password, "fixture-token-sensitive"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("secret leaked in error: %v", err)
		}
	}
}

func TestAuthenticationClassification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer server.Close()
	client := &Client{TokenURL: server.URL}
	_, err := client.Token(context.Background(), "fixture-user-sensitive", "fixture-password-sensitive")
	if !IsAuthentication(err) || strings.Contains(err.Error(), "fixture-user-sensitive") || strings.Contains(err.Error(), "fixture-password-sensitive") {
		t.Fatalf("error=%v", err)
	}
}

func TestTLSHostnameToIPAndCertificateValidation(t *testing.T) {
	cert, caPEM := testCertificate(t, "pia.test")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/addKey" || r.URL.Query().Get("pt") == "" || r.URL.Query().Get("pubkey") == "" {
			t.Error("registration request missing fields")
		}
		_, _ = w.Write([]byte(`{"status":"OK","server_key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","server_port":1337,"server_ip":"10.0.0.1","peer_ip":"10.0.0.2","dns_servers":["10.0.0.1"]}`))
	})}
	tlsListener := newTLSListener(listener, cert)
	go server.Serve(tlsListener)
	t.Cleanup(func() { server.Close() })
	caPath := filepath.Join(t.TempDir(), "pia-ca.pem")
	if err := os.WriteFile(caPath, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	client := &Client{CACertPath: caPath, ProbeTimeout: 2 * time.Second}
	candidate := Candidate{IP: "127.0.0.1", Hostname: "pia.test", Port: port}
	if err := client.Probe(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	registration, err := client.Register(context.Background(), candidate, "token-sensitive", "public-key-sensitive")
	if err != nil {
		t.Fatal(err)
	}
	if registration.ServerIP != "10.0.0.1" {
		t.Fatal("registration parse failed")
	}
	candidate.Hostname = "wrong.test"
	if err := client.Probe(context.Background(), candidate); err == nil {
		t.Fatal("certificate hostname validation was disabled")
	}
}

func TestContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &Client{ServerListURL: server.URL}
	if _, err := client.FetchServerList(ctx); err == nil {
		t.Fatal("expected cancellation")
	}
}

func testCertificate(t *testing.T, hostname string) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: hostname}, DNSNames: []string{hostname}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return cert, certPEM
}

func newTLSListener(listener net.Listener, certificate tls.Certificate) net.Listener {
	return tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
}
