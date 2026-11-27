package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProxyHeadersIgnoresUntrustedPeers(t *testing.T) {
	var gotAddr string
	h := proxyHeaders(nil, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotAddr = r.RemoteAddr
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "198.51.100.10:1234"
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	req.Header.Set("X-Forwarded-Proto", "https")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if gotAddr != "198.51.100.10:1234" || req.TLS != nil {
		t.Fatalf("trusted headers from an untrusted peer: addr=%q tls=%v", gotAddr, req.TLS)
	}
}

func TestProxyHeadersTrustsConfiguredProxy(t *testing.T) {
	var gotAddr string
	var gotTLS bool
	h := proxyHeaders([]string{"10.0.0.0/8"}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotAddr = r.RemoteAddr
		gotTLS = r.TLS != nil
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.5:443"
	// Right-most untrusted hop wins; the injected left-most value is ignored.
	req.Header.Set("X-Forwarded-For", "127.0.0.1, 192.0.2.44, 10.0.0.9")
	req.Header.Set("X-Forwarded-Proto", "https")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if gotAddr != "192.0.2.44:443" {
		t.Fatalf("RemoteAddr = %q, want client hop 192.0.2.44:443", gotAddr)
	}
	if !gotTLS {
		t.Fatal("X-Forwarded-Proto=https from a trusted proxy must mark the request as TLS")
	}
}

func TestConcurrencyLimit(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 2)
	h := concurrencyLimit(1, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))

	done := make(chan struct{})
	first := httptest.NewRecorder()
	go func() {
		h.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/", nil))
		close(done)
	}()
	<-entered

	second := httptest.NewRecorder()
	h.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/", nil))
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("second request status = %d, want 503", second.Code)
	}
	close(release)
	<-done
}
