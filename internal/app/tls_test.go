package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"githost.dev/githost/internal/config"
)

// generateCert writes a self-signed certificate/key pair for testing.
func generateCert(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "githost-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func TestNativeTLS(t *testing.T) {
	cfg := testConfig(t) // HTTPPort 0 = ephemeral (not validated here)
	certPath, keyPath := generateCert(t, t.TempDir())
	cfg.Server.TLS = config.TLS{Enabled: true, CertFile: certPath, KeyFile: keyPath}

	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Init(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	srv, err := Start(ctx, cfg, logger)
	if err != nil {
		t.Fatalf("Start with TLS: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		srv.Stop(stopCtx)
	}()

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // test CA
		},
	}
	resp, err := client.Get("https://" + srv.Addr().String() + "/healthz")
	if err != nil {
		t.Fatalf("GET https healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz over TLS = %d", resp.StatusCode)
	}
	if resp.TLS == nil || resp.TLS.HandshakeComplete != true {
		t.Fatal("response did not ride on TLS")
	}
}

func TestNativeTLSRequiresReadableCert(t *testing.T) {
	cfg := testConfig(t)
	certPath, keyPath := generateCert(t, t.TempDir())
	// Enabled, but the certificate file was removed after validation.
	cfg.Server.TLS = config.TLS{Enabled: true, CertFile: certPath, KeyFile: keyPath}
	os.Remove(certPath)

	ctx := context.Background()
	if err := Init(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("Start must fail when the certificate is unreadable")
	}
}

func TestTLSValidationRejectsIncomplete(t *testing.T) {
	cfg := config.Default()
	if err := cfg.Resolve(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	cfg.Server.HTTPPort = 3100                 // validation requires a real port
	cfg.Server.TLS = config.TLS{Enabled: true} // no cert/key
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "server.tls") {
		t.Fatalf("err = %v, want server.tls validation error", err)
	}
}
