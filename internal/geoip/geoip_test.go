package geoip_test

import (
	"context"
	"testing"

	"texlite-share/internal/geoip"
)

func TestLookup_LocalAndPrivate(t *testing.T) {
	tests := []struct {
		ip       string
		expected string
	}{
		{"127.0.0.1", "Localhost"},
		{"127.0.0.1:9000", "Localhost"},
		{"::1", "Localhost"},
		{"192.168.1.100", "LAN / Private"},
		{"10.0.0.5", "LAN / Private"},
		{"172.16.0.1", "LAN / Private"},
		{"", "Unknown"},
		{"invalid-ip", "Unknown"},
	}

	for _, tc := range tests {
		got := geoip.Lookup(tc.ip)
		if got != tc.expected {
			t.Errorf("Lookup(%q) = %q, expected %q", tc.ip, got, tc.expected)
		}
	}
}

func TestLookupWithContext(t *testing.T) {
	ctx := context.Background()
	got := geoip.LookupWithContext(ctx, "127.0.0.1")
	if got != "Localhost" {
		t.Errorf("LookupWithContext failed: got %q", got)
	}
}
