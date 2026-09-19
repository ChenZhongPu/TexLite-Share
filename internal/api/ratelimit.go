package api

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// IPRateLimiter implements a sliding-window in-memory rate limiter per IP.
type IPRateLimiter struct {
	mu      sync.Mutex
	history map[string][]time.Time
	limit   int
	window  time.Duration
}

// NewIPRateLimiter creates a new rate limiter with the given request limit per duration window.
func NewIPRateLimiter(limit int, window time.Duration) *IPRateLimiter {
	return &IPRateLimiter{
		history: make(map[string][]time.Time),
		limit:   limit,
		window:  window,
	}
}

// Allow checks if a request from the given IP is permitted under the rate limit.
func (l *IPRateLimiter) Allow(ip string) bool {
	if l.limit <= 0 {
		return true // rate limiting disabled
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-l.window)

	// Periodic cleanup of stale IP records if map grows large
	if len(l.history) > 1000 {
		for k, v := range l.history {
			if len(v) == 0 || v[len(v)-1].Before(cutoff) {
				delete(l.history, k)
			}
		}
	}

	// Filter timestamps within current window
	times := l.history[ip]
	valid := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}

	if len(valid) >= l.limit {
		l.history[ip] = valid
		return false
	}

	l.history[ip] = append(valid, now)
	return true
}

// ExtractClientIP extracts the real client IP address from the HTTP request,
// taking into account X-Forwarded-For if present (from Caddy / reverse proxies).
func ExtractClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			ip := strings.TrimSpace(parts[0])
			if net.ParseIP(ip) != nil {
				return ip
			}
		}
	}

	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		ip := strings.TrimSpace(xri)
		if net.ParseIP(ip) != nil {
			return ip
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
