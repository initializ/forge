package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateToken(t *testing.T) {
	token, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken() error: %v", err)
	}
	if len(token) == 0 {
		t.Fatal("GenerateToken() returned empty string")
	}
	// 32 bytes → 43 chars in base64 raw URL encoding
	if len(token) != 43 {
		t.Errorf("expected token length 43, got %d", len(token))
	}

	// Ensure uniqueness
	token2, _ := GenerateToken()
	if token == token2 {
		t.Error("two generated tokens should not be equal")
	}
}

func TestValidateToken(t *testing.T) {
	tests := []struct {
		name      string
		presented string
		expected  string
		want      bool
	}{
		{"matching", "abc123", "abc123", true},
		{"mismatch", "abc123", "xyz789", false},
		{"empty presented", "", "abc123", false},
		{"empty expected", "abc123", "", false},
		{"both empty", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidateToken(tt.presented, tt.expected); got != tt.want {
				t.Errorf("ValidateToken(%q, %q) = %v, want %v", tt.presented, tt.expected, got, tt.want)
			}
		})
	}
}

func TestStoreAndLoadToken(t *testing.T) {
	dir := t.TempDir()

	token := "test-token-value"
	if err := StoreToken(dir, token); err != nil {
		t.Fatalf("StoreToken() error: %v", err)
	}

	// Verify file permissions
	path := filepath.Join(dir, tokenDir, tokenFile)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat token file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("expected file permissions 0600, got %04o", perm)
	}

	// Load and verify
	loaded, err := LoadToken(dir)
	if err != nil {
		t.Fatalf("LoadToken() error: %v", err)
	}
	if loaded != token {
		t.Errorf("LoadToken() = %q, want %q", loaded, token)
	}
}

func TestLoadTokenMissing(t *testing.T) {
	dir := t.TempDir()

	loaded, err := LoadToken(dir)
	if err != nil {
		t.Fatalf("LoadToken() error: %v", err)
	}
	if loaded != "" {
		t.Errorf("expected empty string for missing token, got %q", loaded)
	}
}

func TestTokenPath(t *testing.T) {
	got := TokenPath("/home/user/myagent")
	want := filepath.Join("/home/user/myagent", ".forge", "runtime.token")
	if got != want {
		t.Errorf("TokenPath() = %q, want %q", got, want)
	}
}
