package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"texlite-share/internal/api"
)

func TestIPRateLimiter(t *testing.T) {
	limiter := api.NewIPRateLimiter(3, 100*time.Millisecond)

	ip := "192.168.1.100"

	// 1st, 2nd, 3rd allowed
	if !limiter.Allow(ip) {
		t.Fatal("1st request should be allowed")
	}
	if !limiter.Allow(ip) {
		t.Fatal("2nd request should be allowed")
	}
	if !limiter.Allow(ip) {
		t.Fatal("3rd request should be allowed")
	}

	// 4th denied (rate limit exceeded)
	if limiter.Allow(ip) {
		t.Fatal("4th request should be denied")
	}

	// Different IP allowed
	if !limiter.Allow("192.168.1.101") {
		t.Fatal("different IP should be allowed")
	}

	// Wait for window to expire
	time.Sleep(120 * time.Millisecond)

	// Now allowed again
	if !limiter.Allow(ip) {
		t.Fatal("request after window should be allowed")
	}
}

func TestExtractClientIP(t *testing.T) {
	tests := []struct {
		name       string
		headers    map[string]string
		remoteAddr string
		expectedIP string
	}{
		{
			name:       "remote addr with port",
			remoteAddr: "1.2.3.4:5678",
			expectedIP: "1.2.3.4",
		},
		{
			name: "x-forwarded-for single",
			headers: map[string]string{
				"X-Forwarded-For": "5.6.7.8",
			},
			remoteAddr: "127.0.0.1:9000",
			expectedIP: "5.6.7.8",
		},
		{
			name: "x-forwarded-for multiple proxies",
			headers: map[string]string{
				"X-Forwarded-For": "10.0.0.1, 192.168.1.1",
			},
			remoteAddr: "127.0.0.1:9000",
			expectedIP: "10.0.0.1",
		},
		{
			name: "x-real-ip header",
			headers: map[string]string{
				"X-Real-IP": "9.8.7.6",
			},
			remoteAddr: "127.0.0.1:9000",
			expectedIP: "9.8.7.6",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.remoteAddr
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			ip := api.ExtractClientIP(req)
			if ip != tc.expectedIP {
				t.Errorf("expected IP %q, got %q", tc.expectedIP, ip)
			}
		})
	}
}
