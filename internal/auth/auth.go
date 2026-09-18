package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"

	"texlite-share/internal/protocol"
)

// GenerateToken generates a cryptographically secure 256-bit (32 bytes) random token
// encoded in unpadded URL-safe Base64.
func GenerateToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("failed to generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// HashToken computes the SHA-256 hash of a plaintext token.
func HashToken(token string) [32]byte {
	return sha256.Sum256([]byte(token))
}

// VerifyToken compares a plaintext token with its expected SHA-256 hash in constant time.
func VerifyToken(token string, expectedHash []byte) bool {
	if len(expectedHash) != 32 || token == "" {
		return false
	}
	actual := HashToken(token)
	return subtle.ConstantTimeCompare(actual[:], expectedHash) == 1
}

// ExtractBearerToken extracts the Bearer token from an HTTP request's Authorization header.
func ExtractBearerToken(r *http.Request) (string, error) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return "", protocol.ErrInvalidToken
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", protocol.ErrInvalidToken
	}

	token := strings.TrimSpace(parts[1])
	if token == "" {
		return "", protocol.ErrInvalidToken
	}
	return token, nil
}
