package controlplane

import (
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// loopbackHosts are the only Host names a loopback control plane answers to.
var loopbackHosts = []string{"localhost", "127.0.0.1", "[::1]"}

// isLoopbackListen reports whether listen binds only the loopback interface.
func isLoopbackListen(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		host = listen
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// isLoopbackHost reports whether a Host header names this machine's loopback
// interface: localhost, 127.0.0.1 or [::1], with or without a port.
func isLoopbackHost(host string) bool {
	name := host
	if i := strings.LastIndexByte(host, ':'); i >= 0 && !strings.HasSuffix(host, "]") {
		name = host[:i]
		if !isPort(host[i+1:]) {
			return false
		}
	}
	for _, allowed := range loopbackHosts {
		if strings.EqualFold(name, allowed) {
			return true
		}
	}
	return false
}

// publicURLHost returns the host[:port] from a configured server.public_url.
func publicURLHost(publicURL string) string {
	if publicURL == "" {
		return ""
	}
	parsed, err := url.Parse(publicURL)
	if err != nil {
		return ""
	}
	return parsed.Host
}

// isPublicNonLoopback reports whether server.public_url is configured to a
// non-loopback host.
func isPublicNonLoopback(publicURL string) bool {
	host := publicURLHost(publicURL)
	return host != "" && !isLoopbackHost(host)
}

// isPort reports whether value is a decimal TCP port number.
func isPort(value string) bool {
	if value == "" || strings.Trim(value, "0123456789") != "" {
		return false
	}
	port, err := strconv.Atoi(value)
	return err == nil && port <= 65535
}

// loopbackHostOnly refuses requests whose Host header is not a loopback name
// (or the configured server.public_url host, so a local reverse proxy such as
// Caddy or nginx can forward traffic to a loopback listener).
func loopbackHostOnly(allowedPublicHost string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackHost(r.Host) && (allowedPublicHost == "" || !strings.EqualFold(r.Host, allowedPublicHost)) {
			http.Error(w, "misdirected request: this control plane only answers to localhost, 127.0.0.1 or [::1]", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// canonicalHostOnly enforces that browser requests on a public deployment
// arrive on the canonical server.public_url host so Cloud Run's *.run.app
// alias cannot break __Host- OAuth cookies. GET /healthz, loopback hosts
// (used by local CLI commands such as machinist reload even on wildcard
// listeners), and bearer-authenticated API calls are exempt.
func canonicalHostOnly(publicURL string, next http.Handler) http.Handler {
	canonicalHost := publicURLHost(publicURL)
	if canonicalHost == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		if strings.EqualFold(r.Host, canonicalHost) || isLoopbackHost(r.Host) {
			next.ServeHTTP(w, r)
			return
		}
		isAPI := strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/machinist/") || r.URL.Path == "/mcp"
		if isAPI && hasBearerHeader(r) {
			next.ServeHTTP(w, r)
			return
		}
		// Redirect plain browser navigations (GET/HEAD) on the SPA root or
		// /auth/github/start to the canonical URL; reject callbacks and API
		// requests with 421 Misdirected Request.
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
			!isAPI &&
			r.URL.Path != "/auth/github/callback" {
			target := strings.TrimRight(publicURL, "/") + r.URL.RequestURI()
			http.Redirect(w, r, target, http.StatusTemporaryRedirect)
			return
		}
		http.Error(w, "misdirected request: use the canonical server.public_url host", http.StatusMisdirectedRequest)
	})
}
