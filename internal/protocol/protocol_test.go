package protocol_test

import (
	"net/http"
	"testing"

	"texlite-share/internal/protocol"
)

func TestGenerateAndValidateShareID(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id, err := protocol.GenerateShareID()
		if err != nil {
			t.Fatalf("GenerateShareID failed: %v", err)
		}
		if len(id) != 16 {
			t.Fatalf("expected 16 chars, got %d for %q", len(id), id)
		}
		if err := protocol.ValidateShareID(id); err != nil {
			t.Fatalf("ValidateShareID failed for generated id %q: %v", id, err)
		}
		if seen[id] {
			t.Fatalf("collision detected for id %q", id)
		}
		seen[id] = true
	}
}

func TestValidateShareID_EdgeCases(t *testing.T) {
	tests := []struct {
		id    string
		valid bool
	}{
		{"k83fx2m7pq4z7abc", true},
		{"abc12345", true},
		{"short", false},
		{"waytoolongstringexceedingthirtytwocharacters1234567890", false},
		{"k83fx2_abc", false},
		{"k83fx2-abc", true},
		{"-k83fx2abc", false},
		{"k83fx2abc-", false},
		{"k83FX2m7pq4z7abc", false}, // uppercase rejected
		{"../etc/passwd", false},
		{"id with space", false},
		{"", false},
	}

	for _, tc := range tests {
		err := protocol.ValidateShareID(tc.id)
		if tc.valid && err != nil {
			t.Errorf("expected %q to be valid, got: %v", tc.id, err)
		} else if !tc.valid && err == nil {
			t.Errorf("expected %q to be invalid, got nil error", tc.id)
		}
	}
}

func TestErrorToHTTPStatus(t *testing.T) {
	cases := []struct {
		err      error
		expected int
	}{
		{protocol.ErrInvalidShare, http.StatusNotFound},
		{protocol.ErrShareNotFound, http.StatusNotFound},
		{protocol.ErrInvalidToken, http.StatusUnauthorized},
		{protocol.ErrRevoked, http.StatusForbidden},
		{protocol.ErrExpired, http.StatusGone},
		{protocol.ErrCapacityExceeded, http.StatusServiceUnavailable},
		{protocol.ErrTunnelOffline, http.StatusServiceUnavailable},
		{protocol.ErrTooManyStreams, http.StatusTooManyRequests},
		{protocol.ErrProtocolMismatch, http.StatusBadRequest},
		{protocol.ErrInvalidLocalAddr, http.StatusBadRequest},
	}

	for _, c := range cases {
		if status := protocol.ErrorToHTTPStatus(c.err); status != c.expected {
			t.Errorf("for error %v, expected %d, got %d", c.err, c.expected, status)
		}
	}
}
