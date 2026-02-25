package oauth

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveAndLoadCredentials(t *testing.T) {
	// Use a temp directory
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tmpDir)
	defer func() { _ = os.Setenv("HOME", origHome) }()

	token := &Token{
		AccessToken:  "test-access-token",
		RefreshToken: "test-refresh-token",
		TokenType:    "Bearer",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
	}

	// Save
	err := SaveCredentials("testprovider", token)
	if err != nil {
		t.Fatalf("SaveCredentials() error: %v", err)
	}

	// Verify file exists with correct permissions
	credPath := filepath.Join(tmpDir, ".forge", "credentials", "testprovider.json")
	info, err := os.Stat(credPath)
	if err != nil {
		t.Fatalf("credential file not found: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("expected permissions 0600, got %o", info.Mode().Perm())
	}

	// Load
	loaded, err := LoadCredentials("testprovider")
	if err != nil {
		t.Fatalf("LoadCredentials() error: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected non-nil token")
	}
	if loaded.AccessToken != "test-access-token" {
		t.Errorf("expected access token 'test-access-token', got %q", loaded.AccessToken)
	}
	if loaded.RefreshToken != "test-refresh-token" {
		t.Errorf("expected refresh token 'test-refresh-token', got %q", loaded.RefreshToken)
	}
}

func TestLoadCredentials_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	token, err := LoadCredentials("nonexistent")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if token != nil {
		t.Error("expected nil token for nonexistent provider")
	}
}

func TestDeleteCredentials(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	token := &Token{AccessToken: "delete-me"}
	_ = SaveCredentials("deletable", token)

	err := DeleteCredentials("deletable")
	if err != nil {
		t.Fatalf("DeleteCredentials() error: %v", err)
	}

	loaded, _ := LoadCredentials("deletable")
	if loaded != nil {
		t.Error("expected nil after deletion")
	}
}
