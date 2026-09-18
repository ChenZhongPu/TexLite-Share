package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"texlite-share/internal/auth"
)

func TestGenerateTokenAndVerify(t *testing.T) {
	for i := 0; i < 100; i++ {
		token, err := auth.GenerateToken()
		if err != nil {
			t.Fatalf("GenerateToken failed: %v", err)
		}
		if len(token) == 0 {
			t.Fatal("empty token generated")
		}

		hash := auth.HashToken(token)
		if !auth.VerifyToken(token, hash[:]) {
			t.Fatalf("VerifyToken failed for generated token %q", token)
		}

		// Verify that wrong token fails
		wrongToken := token + "x"
		if auth.VerifyToken(wrongToken, hash[:]) {
			t.Fatalf("VerifyToken succeeded for invalid token %q", wrongToken)
		}

		// Verify invalid hash length fails safely
		if auth.VerifyToken(token, []byte("short")) {
			t.Fatal("VerifyToken succeeded for short hash")
		}
	}
}

func TestExtractBearerToken(t *testing.T) {
	tests := []struct {
		name        string
		authHeader  string
		expected    string
		expectError bool
	}{
		{"valid Bearer", "Bearer my-secret-token", "my-secret-token", false},
		{"case insensitive bearer", "bearer my-secret-token", "my-secret-token", false},
		{"extra spaces", "Bearer   my-secret-token  ", "my-secret-token", false},
		{"missing header", "", "", true},
		{"empty bearer", "Bearer ", "", true},
		{"wrong scheme", "Basic dXNlcjpwYXNz", "", true},
		{"single word", "Bearer", "", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			token, err := auth.ExtractBearerToken(req)
			if tc.expectError {
				if err == nil {
					t.Errorf("expected error, got token %q", token)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if token != tc.expected {
					t.Errorf("expected %q, got %q", tc.expected, token)
				}
			}
		})
	}
}
