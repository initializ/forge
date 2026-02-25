package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

func TestGeneratePKCE(t *testing.T) {
	pkce, err := GeneratePKCE()
	if err != nil {
		t.Fatalf("GeneratePKCE() error: %v", err)
	}

	// Verifier should be base64url-encoded 32 bytes = 43 chars
	if len(pkce.Verifier) != 43 {
		t.Errorf("expected verifier length 43, got %d", len(pkce.Verifier))
	}

	// Method must be S256
	if pkce.Method != "S256" {
		t.Errorf("expected method S256, got %s", pkce.Method)
	}

	// Verify the challenge matches the verifier
	hash := sha256.Sum256([]byte(pkce.Verifier))
	expectedChallenge := base64.RawURLEncoding.EncodeToString(hash[:])
	if pkce.Challenge != expectedChallenge {
		t.Errorf("challenge mismatch: got %s, want %s", pkce.Challenge, expectedChallenge)
	}
}

func TestGeneratePKCE_Uniqueness(t *testing.T) {
	p1, _ := GeneratePKCE()
	p2, _ := GeneratePKCE()
	if p1.Verifier == p2.Verifier {
		t.Error("two PKCE params should not have the same verifier")
	}
}

func TestGenerateState(t *testing.T) {
	state, err := GenerateState()
	if err != nil {
		t.Fatalf("GenerateState() error: %v", err)
	}
	if state == "" {
		t.Error("state should not be empty")
	}
	// 16 bytes base64url = 22 chars
	if len(state) != 22 {
		t.Errorf("expected state length 22, got %d", len(state))
	}
}
