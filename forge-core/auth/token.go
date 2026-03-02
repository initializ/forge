package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
)

const (
	// tokenBytes is the number of random bytes for token generation (256-bit).
	tokenBytes = 32

	// tokenDir is the subdirectory under the agent root where runtime files are stored.
	tokenDir = ".forge"

	// tokenFile is the filename for the stored bearer token.
	tokenFile = "runtime.token"
)

// GenerateToken creates a cryptographically random bearer token.
// Returns a URL-safe base64-encoded string with 256 bits of entropy.
func GenerateToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ValidateToken compares a presented token against the expected token
// using constant-time comparison to prevent timing attacks.
func ValidateToken(presented, expected string) bool {
	if len(presented) == 0 || len(expected) == 0 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) == 1
}

// TokenPath returns the path to the token file for the given agent root directory.
func TokenPath(agentRoot string) string {
	return filepath.Join(agentRoot, tokenDir, tokenFile)
}

// StoreToken writes a token to <agentRoot>/.forge/runtime.token with 0600 permissions.
func StoreToken(agentRoot, token string) error {
	dir := filepath.Join(agentRoot, tokenDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating token directory: %w", err)
	}
	path := filepath.Join(dir, tokenFile)
	if err := os.WriteFile(path, []byte(token), 0600); err != nil {
		return fmt.Errorf("writing token file: %w", err)
	}
	return setFileOwnerOnly(path)
}

// LoadToken reads the stored token from <agentRoot>/.forge/runtime.token.
// Returns ("", nil) if the file does not exist.
func LoadToken(agentRoot string) (string, error) {
	path := filepath.Join(agentRoot, tokenDir, tokenFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading token file: %w", err)
	}
	return string(data), nil
}
