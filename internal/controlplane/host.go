package controlplane

import (
	"net"
	"net/http"
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

// isPort reports whether value is a decimal TCP port number.
func isPort(value string) bool {
	if value == "" || strings.Trim(value, "0123456789") != "" {
		return false
	}
	port, err := strconv.Atoi(value)
	return err == nil && port <= 65535
}

// loopbackHostOnly refuses requests whose Host header is not a loopback name.
// A browser page on a rebound domain sends that domain as its Host, so this
// keeps such pages away from a control plane listening on loopback.
func loopbackHostOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackHost(r.Host) {
			http.Error(w, "misdirected request: this control plane only answers to localhost, 127.0.0.1 or [::1]", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}
