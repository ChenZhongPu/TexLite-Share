package config_test

import (
	"testing"

	"texlite-share/internal/config"
)

func TestValidateLocalAddr(t *testing.T) {
	tests := []struct {
		addr  string
		valid bool
	}{
		{"127.0.0.1:3000", true},
		{"127.0.0.1:8080", true},
		{"localhost:3000", true},
		{"localhost:80", true},
		{"[::1]:3000", true},
		{"127.0.0.2:3000", true}, // 127.0.0.0/8 loopback block
		{"0.0.0.0:3000", false},
		{"192.168.1.1:3000", false},
		{"10.0.0.1:3000", false},
		{"172.16.0.1:3000", false},
		{"8.8.8.8:3000", false},
		{"example.com:3000", false},
		{"3000", false},
		{"", false},
	}

	for _, tc := range tests {
		err := config.ValidateLocalAddr(tc.addr)
		if tc.valid && err != nil {
			t.Errorf("expected %q to be valid, got: %v", tc.addr, err)
		} else if !tc.valid && err == nil {
			t.Errorf("expected %q to be rejected, got nil error", tc.addr)
		}
	}
}

func TestDeriveWebSocketURL(t *testing.T) {
	tests := []struct {
		serverURL string
		shareID   string
		expected  string
		hasError  bool
	}{
		{"http://127.0.0.1:9000", "abc12345", "ws://127.0.0.1:9000/api/v1/tunnel/abc12345", false},
		{"https://share.example.com", "xyz98765", "wss://share.example.com/api/v1/tunnel/xyz98765", false},
		{"ws://127.0.0.1:9000", "abc12345", "ws://127.0.0.1:9000/api/v1/tunnel/abc12345", false},
		{"wss://share.example.com", "xyz98765", "wss://share.example.com/api/v1/tunnel/xyz98765", false},
		{"http://127.0.0.1:9000/some/path?foo=bar", "abc", "ws://127.0.0.1:9000/api/v1/tunnel/abc", false},
		{"ftp://bad.com", "abc", "", true},
	}

	for _, tc := range tests {
		actual, err := config.DeriveWebSocketURL(tc.serverURL, tc.shareID)
		if tc.hasError {
			if err == nil {
				t.Errorf("expected error for %q, got: %q", tc.serverURL, actual)
			}
		} else {
			if err != nil {
				t.Errorf("unexpected error for %q: %v", tc.serverURL, err)
			}
			if actual != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, actual)
			}
		}
	}
}
