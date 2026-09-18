package proxy_test

import (
	"testing"

	"texlite-share/internal/proxy"
)

func TestExtractShareID(t *testing.T) {
	tests := []struct {
		host        string
		baseDomain  string
		expectedID  string
		expectError bool
	}{
		{"abc12345.share.local", "share.local", "abc12345", false},
		{"abc12345.share.local:9000", "share.local", "abc12345", false},
		{"k83fx2m7pq4z7abc.share.example.com", "share.example.com", "k83fx2m7pq4z7abc", false},
		{"k83fx2m7pq4z7abc.share.example.com:443", "share.example.com", "k83fx2m7pq4z7abc", false},
		{"K83FX2M7PQ4Z7ABC.SHARE.LOCAL:9000", "share.local", "k83fx2m7pq4z7abc", false}, // case insensitivity
		{"share.local", "share.local", "", true},
		{"share.local:9000", "share.local", "", true},
		{"foo.bar.share.local", "share.local", "", true},
		{"bad_id_12345.share.local", "share.local", "", true}, // invalid characters
		{"short.share.local", "share.local", "", true},         // too short
		{"randomdomain.com", "share.local", "", true},
		{"127.0.0.1:9000", "share.local", "", true},
		{"", "share.local", "", true},
	}

	for _, tc := range tests {
		id, err := proxy.ExtractShareID(tc.host, tc.baseDomain)
		if tc.expectError {
			if err == nil {
				t.Errorf("expected error for host %q (base %q), got ID %q", tc.host, tc.baseDomain, id)
			}
		} else {
			if err != nil {
				t.Errorf("unexpected error for host %q (base %q): %v", tc.host, tc.baseDomain, err)
			}
			if id != tc.expectedID {
				t.Errorf("expected ID %q, got %q for host %q", tc.expectedID, id, tc.host)
			}
		}
	}
}
