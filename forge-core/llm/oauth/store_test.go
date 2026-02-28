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
	t.Setenv("HOME", tmpDir)
	t.Setenv("FORGE_PASSPHRASE", "") // ensure plaintext path

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
	t.Setenv("FORGE_PASSPHRASE", "")

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
	t.Setenv("FORGE_PASSPHRASE", "")

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

func TestSaveCredentials_Encrypted(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("FORGE_PASSPHRASE", "test-passphrase")

	token := &Token{
		AccessToken:  "enc-access",
		RefreshToken: "enc-refresh",
		TokenType:    "Bearer",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
	}

	// Save with encryption
	if err := SaveCredentials("openai", token); err != nil {
		t.Fatalf("SaveCredentials(encrypted) error: %v", err)
	}

	// Plaintext file should NOT exist
	credPath := filepath.Join(tmpDir, ".forge", "credentials", "openai.json")
	if _, err := os.Stat(credPath); !os.IsNotExist(err) {
		t.Error("expected no plaintext file when encrypted store is used")
	}

	// Encrypted file should exist
	encPath := filepath.Join(tmpDir, ".forge", "secrets.enc")
	if _, err := os.Stat(encPath); err != nil {
		t.Fatalf("encrypted file not found: %v", err)
	}

	// Load back
	loaded, err := LoadCredentials("openai")
	if err != nil {
		t.Fatalf("LoadCredentials(encrypted) error: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected non-nil token")
	}
	if loaded.AccessToken != "enc-access" {
		t.Errorf("expected 'enc-access', got %q", loaded.AccessToken)
	}
	if loaded.RefreshToken != "enc-refresh" {
		t.Errorf("expected 'enc-refresh', got %q", loaded.RefreshToken)
	}
}

func TestLoadCredentials_FallbackToPlaintext(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	token := &Token{
		AccessToken:  "plaintext-token",
		RefreshToken: "plaintext-refresh",
		TokenType:    "Bearer",
	}

	// Save as plaintext (no passphrase)
	t.Setenv("FORGE_PASSPHRASE", "")
	if err := SaveCredentials("openai", token); err != nil {
		t.Fatalf("SaveCredentials(plaintext) error: %v", err)
	}

	// Now set passphrase — Load should fall back to plaintext since the key
	// is not in the encrypted store.
	t.Setenv("FORGE_PASSPHRASE", "test-passphrase")

	loaded, err := LoadCredentials("openai")
	if err != nil {
		t.Fatalf("LoadCredentials(fallback) error: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected non-nil token from plaintext fallback")
	}
	if loaded.AccessToken != "plaintext-token" {
		t.Errorf("expected 'plaintext-token', got %q", loaded.AccessToken)
	}
}

func TestMigrateToEncrypted(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	token := &Token{
		AccessToken:  "migrate-me",
		RefreshToken: "migrate-refresh",
		TokenType:    "Bearer",
	}

	// Save as plaintext
	t.Setenv("FORGE_PASSPHRASE", "")
	if err := SaveCredentials("openai", token); err != nil {
		t.Fatalf("SaveCredentials(plaintext) error: %v", err)
	}

	// Plaintext file should exist
	credPath := filepath.Join(tmpDir, ".forge", "credentials", "openai.json")
	if _, err := os.Stat(credPath); err != nil {
		t.Fatalf("plaintext file not found before migration: %v", err)
	}

	// Migrate with passphrase
	t.Setenv("FORGE_PASSPHRASE", "test-passphrase")
	if err := MigrateToEncrypted("openai"); err != nil {
		t.Fatalf("MigrateToEncrypted() error: %v", err)
	}

	// Plaintext file should be deleted
	if _, err := os.Stat(credPath); !os.IsNotExist(err) {
		t.Error("expected plaintext file to be deleted after migration")
	}

	// Load from encrypted store
	loaded, err := LoadCredentials("openai")
	if err != nil {
		t.Fatalf("LoadCredentials(post-migration) error: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected non-nil token after migration")
	}
	if loaded.AccessToken != "migrate-me" {
		t.Errorf("expected 'migrate-me', got %q", loaded.AccessToken)
	}
	if loaded.RefreshToken != "migrate-refresh" {
		t.Errorf("expected 'migrate-refresh', got %q", loaded.RefreshToken)
	}
}

func TestDeleteCredentials_Both(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	token := &Token{
		AccessToken: "both-token",
		TokenType:   "Bearer",
	}

	// Save plaintext first
	t.Setenv("FORGE_PASSPHRASE", "")
	if err := SaveCredentials("openai", token); err != nil {
		t.Fatalf("SaveCredentials(plaintext) error: %v", err)
	}

	// Save encrypted (this also removes plaintext, so re-create it)
	t.Setenv("FORGE_PASSPHRASE", "test-passphrase")
	if err := SaveCredentials("openai", token); err != nil {
		t.Fatalf("SaveCredentials(encrypted) error: %v", err)
	}

	// Manually recreate the plaintext file to simulate both existing
	credPath := filepath.Join(tmpDir, ".forge", "credentials", "openai.json")
	_ = os.MkdirAll(filepath.Dir(credPath), 0o700)
	_ = os.WriteFile(credPath, []byte(`{"access_token":"both-token"}`), 0o600)

	// Delete should remove both
	if err := DeleteCredentials("openai"); err != nil {
		t.Fatalf("DeleteCredentials() error: %v", err)
	}

	// Plaintext should be gone
	if _, err := os.Stat(credPath); !os.IsNotExist(err) {
		t.Error("expected plaintext file to be deleted")
	}

	// Encrypted should be gone — Load should return nil
	loaded, err := LoadCredentials("openai")
	if err != nil {
		t.Fatalf("LoadCredentials(after delete) error: %v", err)
	}
	if loaded != nil {
		t.Error("expected nil token after deleting both stores")
	}
}
