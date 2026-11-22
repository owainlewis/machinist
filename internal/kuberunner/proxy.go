package kuberunner

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// PublicAddress excludes internal, metadata, multicast and reserved networks.
func PublicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32"} {
		if netip.MustParsePrefix(cidr).Contains(ip) {
			return false
		}
	}
	return true
}

func ProxyHandler(allowed []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, port, err := net.SplitHostPort(r.Host)
		if r.Method != "CONNECT" || err != nil || port != "443" {
			http.Error(w, "CONNECT to approved HTTPS destinations only", http.StatusForbidden)
			return
		}
		host = strings.ToLower(host)
		permitted := false
		for _, name := range allowed {
			if host == name {
				permitted = true
			}
		}
		if !permitted {
			http.Error(w, "destination is not approved", http.StatusForbidden)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(ips) == 0 {
			http.Error(w, "destination unavailable", http.StatusBadGateway)
			return
		}
		for _, ip := range ips {
			if !PublicAddress(ip) {
				http.Error(w, "internal destination denied", http.StatusForbidden)
				return
			}
		}
		upstream, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(ips[0].String(), port))
		if err != nil {
			http.Error(w, "destination unavailable", http.StatusBadGateway)
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			upstream.Close()
			http.Error(w, "tunnel unavailable", 500)
			return
		}
		client, buffer, err := hijacker.Hijack()
		if err != nil {
			upstream.Close()
			return
		}
		defer client.Close()
		defer upstream.Close()
		_, _ = fmt.Fprint(buffer, "HTTP/1.1 200 Connection Established\r\n\r\n")
		if buffer.Flush() != nil {
			return
		}
		_ = client.SetDeadline(time.Now().Add(40 * time.Minute))
		_ = upstream.SetDeadline(time.Now().Add(40 * time.Minute))
		done := make(chan struct{})
		go func() {
			_, _ = io.Copy(upstream, buffer)
			if t, ok := upstream.(*net.TCPConn); ok {
				_ = t.CloseWrite()
			}
			close(done)
		}()
		_, _ = io.Copy(client, upstream)
		_ = client.Close()
		_ = upstream.Close()
		<-done
	})
}

func ServeProxy(ctx context.Context, address string, allowed []string) error {
	server := &http.Server{Addr: address, Handler: ProxyHandler(allowed), ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 8192}
	go func() { <-ctx.Done(); _ = server.Close() }()
	err := server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
