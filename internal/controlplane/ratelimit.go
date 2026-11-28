package controlplane

import (
	"container/list"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	authRateLimitMaxEntries = 10000
	authRateLimitBurst      = 10.0
	authRateLimitPerSec     = 20.0 / 60.0 // 20 req/min per IP
)

// clientIP extracts the caller IP respecting server.trusted_proxy_hops.
// With trustedProxyHops == 0 (the default), X-Forwarded-For is ignored so a
// direct VM cannot be spoofed. With N > 0 trusted hops, it combines all
// X-Forwarded-For header values in order (RFC 9110 §5.3) and selects the IP
// N hops from the right, falling back to r.RemoteAddr.
func clientIP(r *http.Request, trustedProxyHops int) string {
	if r == nil {
		return ""
	}
	if trustedProxyHops > 0 {
		if xffValues := r.Header.Values("X-Forwarded-For"); len(xffValues) > 0 {
			ips := make([]string, 0, len(xffValues))
			for _, value := range xffValues {
				for _, part := range strings.Split(value, ",") {
					candidate := strings.TrimSpace(part)
					if ip := net.ParseIP(candidate); ip != nil {
						ips = append(ips, ip.String())
					}
				}
			}
			if len(ips) >= trustedProxyHops {
				return ips[len(ips)-trustedProxyHops]
			}
			if len(ips) > 0 {
				return ips[0]
			}
		}
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return host
}

type rateBucket struct {
	key        string
	tokens     float64
	lastRefill time.Time
}

// ipRateLimiter is a bounded LRU token-bucket rate limiter keyed by client IP.
type ipRateLimiter struct {
	mu         sync.Mutex
	maxEntries int
	burst      float64
	perSec     float64
	now        func() time.Time
	entries    map[string]*list.Element
	order      *list.List
}

func newIPRateLimiter(maxEntries int, perSec, burst float64, now func() time.Time) *ipRateLimiter {
	if now == nil {
		now = time.Now
	}
	return &ipRateLimiter{
		maxEntries: maxEntries,
		burst:      burst,
		perSec:     perSec,
		now:        now,
		entries:    make(map[string]*list.Element),
		order:      list.New(),
	}
}

func (l *ipRateLimiter) Allow(key string) bool {
	if key == "" {
		key = "unknown"
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	if elem, ok := l.entries[key]; ok {
		b := elem.Value.(*rateBucket)
		l.order.MoveToFront(elem)
		elapsed := now.Sub(b.lastRefill).Seconds()
		if elapsed > 0 {
			b.tokens += elapsed * l.perSec
			if b.tokens > l.burst {
				b.tokens = l.burst
			}
			b.lastRefill = now
		}
		if b.tokens < 1.0 {
			return false
		}
		b.tokens -= 1.0
		return true
	}

	if l.order.Len() >= l.maxEntries {
		oldest := l.order.Back()
		if oldest != nil {
			l.order.Remove(oldest)
			delete(l.entries, oldest.Value.(*rateBucket).key)
		}
	}

	b := &rateBucket{
		key:        key,
		tokens:     l.burst - 1.0,
		lastRefill: now,
	}
	elem := l.order.PushFront(b)
	l.entries[key] = elem
	return true
}
