package owaspasi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/secrets"
)

// ASI03 — Identity & Privilege Abuse. Grade: Partial.
//
// Enforced portion: agent credentials are isolated at rest. A secret written
// through the encrypted-file provider is not recoverable in plaintext from the
// backing file. The instrumented signal is the on-disk ciphertext not
// containing the secret value. Guideline: ASI03 #2 (isolate identities).
func TestASI03_SecretsEncryptedAtRest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.enc")
	pass := func() (string, error) { return "correct-horse-battery-staple", nil }
	p := secrets.NewEncryptedFileProvider(path, pass)

	const secretVal = "sk-super-secret-token-value-1234567890"
	if err := p.Set("OPENAI_API_KEY", secretVal); err != nil {
		t.Fatalf("set secret: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read backing file: %v", err)
	}
	if strings.Contains(string(raw), secretVal) {
		t.Error("secret value found in plaintext in the backing file (not encrypted at rest)")
	}

	// Sanity: the value round-trips through the provider.
	got, err := p.Get("OPENAI_API_KEY")
	if err != nil || got != secretVal {
		t.Fatalf("round-trip failed: got %q err %v", got, err)
	}
	t.Log("ASI03 secrets encrypted at rest: ciphertext does not leak the plaintext value")
}

// TestASI03_TaskScopedTokenExpires is the failing target for issue #232:
// forge-core does not mint per-invocation short-lived tokens (Platform lane).
func TestASI03_TaskScopedTokenExpires(t *testing.T) {
	t.Skip("xfail: GAP-TOKEN / issue #232 — task-scoped short-lived tokens (ASI03 #1) " +
		"are a Platform deliverable; forge-core verifies caller tokens only.")
}
