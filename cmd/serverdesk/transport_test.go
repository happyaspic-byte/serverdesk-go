package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestListenerTransportPlainHTTPGuard(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:6005", "[::1]:6005", "localhost:6005"} {
		if err := (listenerTransport{addr: addr}).validate(); err != nil {
			t.Errorf("loopback %q rejected: %v", addr, err)
		}
	}
	for _, addr := range []string{"0.0.0.0:6005", ":6005", "[::]:6005", "192.0.2.10:6005"} {
		err := (listenerTransport{addr: addr}).validate()
		if err == nil || !strings.Contains(err.Error(), "평문 HTTP") {
			t.Errorf("non-loopback %q error = %v, want plaintext guard", addr, err)
		}
		if err := (listenerTransport{addr: addr, allowInsecureHTTP: true}).validate(); err != nil {
			t.Errorf("explicit proxy acknowledgment rejected for %q: %v", addr, err)
		}
	}
}

func TestListenerTransportAddressAndTLSPairValidation(t *testing.T) {
	for _, tc := range []listenerTransport{
		{addr: "6005"},
		{addr: "127.0.0.1:"},
		{addr: "127.0.0.1:70000"},
		{addr: "127.0.0.1:6005", certFile: "cert.pem"},
		{addr: "127.0.0.1:6005", keyFile: "key.pem"},
	} {
		if err := tc.validate(); err == nil {
			t.Fatalf("invalid transport accepted: %+v", tc)
		}
	}
}

func TestListenerTransportTLSAllowsNonLoopback(t *testing.T) {
	certFile, keyFile := writeTestKeyPair(t)
	tc := listenerTransport{addr: "0.0.0.0:6005", certFile: certFile, keyFile: keyFile}
	if err := tc.validate(); err != nil {
		t.Fatalf("valid direct TLS rejected: %v", err)
	}
	if !tc.tlsEnabled() {
		t.Fatal("valid key pair did not enable TLS")
	}
	srv := &http.Server{}
	tc.configureServerTLS(srv)
	if srv.TLSConfig == nil || srv.TLSConfig.MinVersion < tls.VersionTLS12 {
		t.Fatal("direct TLS server does not enforce TLS 1.2 or newer")
	}
	if err := (listenerTransport{addr: "0.0.0.0:6005", certFile: certFile, keyFile: keyFile + ".missing"}).validate(); err == nil {
		t.Fatal("missing TLS key accepted")
	}
}

func TestListenerTransportRejectsInvalidCertificateWindowAndOpenKey(t *testing.T) {
	now := time.Now()
	for name, window := range map[string][2]time.Time{
		"expired": {now.Add(-2 * time.Hour), now.Add(-time.Hour)},
		"not-yet": {now.Add(time.Hour), now.Add(2 * time.Hour)},
	} {
		t.Run(name, func(t *testing.T) {
			certFile, keyFile := writeTestKeyPairFor(t, window[0], window[1])
			err := (listenerTransport{addr: "0.0.0.0:6005", certFile: certFile, keyFile: keyFile}).validate()
			if err == nil || !strings.Contains(err.Error(), "유효기간") {
				t.Fatalf("certificate window error=%v", err)
			}
		})
	}
	if runtime.GOOS != "windows" {
		certFile, keyFile := writeTestKeyPair(t)
		if err := os.Chmod(keyFile, 0o644); err != nil {
			t.Fatal(err)
		}
		err := (listenerTransport{addr: "0.0.0.0:6005", certFile: certFile, keyFile: keyFile}).validate()
		if err == nil || !strings.Contains(err.Error(), "권한") {
			t.Fatalf("open key permissions error=%v", err)
		}
	}
}

func writeTestKeyPair(t *testing.T) (string, string) {
	t.Helper()
	now := time.Now()
	return writeTestKeyPairFor(t, now.Add(-time.Minute), now.Add(time.Hour))
}

func writeTestKeyPairFor(t *testing.T, notBefore, notAfter time.Time) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "serverdesk-test"},
		NotBefore:    notBefore, NotAfter: notAfter,
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}
