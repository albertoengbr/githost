package httpserver

import (
	"crypto/tls"
	"net"
	"net/http"
	"strings"
)

// proxyHeaders trusts X-Forwarded-For / X-Forwarded-Proto only when the
// immediate peer is in the configured CIDR list. With an empty list every
// header is ignored: an attacker cannot spoof their address or make the
// app believe a plaintext connection is HTTPS.
//
// When trusted, XFF is read right-to-left, skipping hops that are also
// trusted, so a client cannot inject a bogus left-most entry. The chosen
// address replaces RemoteAddr for the rest of the handler chain, which is
// what rate limiting and audit records use. X-Forwarded-Proto=https marks
// the request as TLS so cookie Secure and HSTS behave correctly behind a
// TLS-terminating proxy.
func proxyHeaders(trustedCIDRs []string, next http.Handler) http.Handler {
	trusted := parseTrustedCIDRs(trustedCIDRs)
	if len(trusted) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			peer = r.RemoteAddr
		}
		if ip := net.ParseIP(peer); ip == nil || !inAnyCIDR(ip, trusted) {
			next.ServeHTTP(w, r)
			return
		}

		forwardedFor := r.Header.Get("X-Forwarded-For")
		if forwardedFor != "" {
			if chosen := pickClientIP(forwardedFor, trusted); chosen != "" {
				port := "0"
				if _, p, err := net.SplitHostPort(r.RemoteAddr); err == nil {
					port = p
				}
				r.RemoteAddr = net.JoinHostPort(chosen, port)
			}
		}
		if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https") && r.TLS == nil {
			// A minimal non-nil ConnectionState is enough for downstream
			// Secure-cookie and HSTS checks; no handshake data is used.
			r.TLS = &tls.ConnectionState{}
		}
		next.ServeHTTP(w, r)
	})
}

// pickClientIP returns the right-most XFF hop that is not a trusted proxy.
// Invalid entries are skipped rather than trusted.
func pickClientIP(value string, trusted []*net.IPNet) string {
	parts := strings.Split(value, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		candidate := strings.TrimSpace(parts[i])
		if host, _, err := net.SplitHostPort(candidate); err == nil {
			candidate = host
		}
		ip := net.ParseIP(candidate)
		if ip == nil || inAnyCIDR(ip, trusted) {
			continue
		}
		return ip.String()
	}
	return ""
}

func parseTrustedCIDRs(values []string) []*net.IPNet {
	var out []*net.IPNet
	for _, value := range values {
		_, n, err := net.ParseCIDR(value)
		if err != nil {
			continue // config.Validate rejects this at startup
		}
		out = append(out, n)
	}
	return out
}

func inAnyCIDR(ip net.IP, networks []*net.IPNet) bool {
	for _, n := range networks {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
