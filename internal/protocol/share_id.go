package protocol

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"strings"
)

var b32Encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateShareID generates a cryptographically secure 80-bit random share ID
// encoded as a 16-character lowercase base32 string.
func GenerateShareID() (string, error) {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("failed to read random bytes: %w", err)
	}
	return strings.ToLower(b32Encoding.EncodeToString(b[:])), nil
}

// ValidateShareID strictly verifies that the share ID is safe, well-formed,
// and consists exclusively of lowercase alphanumeric characters.
func ValidateShareID(id string) error {
	if len(id) < 8 || len(id) > 32 {
		return fmt.Errorf("%w: length must be between 8 and 32 characters", ErrInvalidShare)
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		isLowerAlpha := c >= 'a' && c <= 'z'
		isDigit := c >= '0' && c <= '9'
		if !isLowerAlpha && !isDigit {
			return fmt.Errorf("%w: contains invalid character '%c'", ErrInvalidShare, c)
		}
	}
	return nil
}
