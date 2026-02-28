package secrets

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptedFileProvider_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.enc")

	pass := func() (string, error) { return "test-passphrase", nil }
	p := NewEncryptedFileProvider(path, pass)

	// Set and retrieve
	if err := p.Set("API_KEY", "sk-123"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := p.Set("DB_PASS", "secret"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	val, err := p.Get("API_KEY")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "sk-123" {
		t.Fatalf("expected 'sk-123', got %q", val)
	}

	// Re-open with a fresh provider (forces re-decrypt from file)
	p2 := NewEncryptedFileProvider(path, pass)
	val2, err := p2.Get("API_KEY")
	if err != nil {
		t.Fatalf("Get after re-open: %v", err)
	}
	if val2 != "sk-123" {
		t.Fatalf("expected 'sk-123' after re-open, got %q", val2)
	}
}

func TestEncryptedFileProvider_WrongPassphrase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.enc")

	goodPass := func() (string, error) { return "correct-horse-battery-staple", nil }
	badPass := func() (string, error) { return "wrong-passphrase", nil }

	p := NewEncryptedFileProvider(path, goodPass)
	if err := p.Set("KEY", "value"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Try to open with wrong passphrase
	p2 := NewEncryptedFileProvider(path, badPass)
	_, err := p2.Get("KEY")
	if err == nil {
		t.Fatal("expected error with wrong passphrase")
	}
}

func TestEncryptedFileProvider_SetGetDeleteList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.enc")

	pass := func() (string, error) { return "pass", nil }
	p := NewEncryptedFileProvider(path, pass)

	// Empty list
	keys, err := p.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("expected 0 keys, got %d", len(keys))
	}

	// Set keys
	if err := p.Set("B", "b-val"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := p.Set("A", "a-val"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// List should be sorted
	keys, err = p.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 2 || keys[0] != "A" || keys[1] != "B" {
		t.Fatalf("expected [A, B], got %v", keys)
	}

	// Delete
	if err := p.Delete("A"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	keys, err = p.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 1 || keys[0] != "B" {
		t.Fatalf("expected [B], got %v", keys)
	}

	// Delete non-existent
	err = p.Delete("NONEXISTENT")
	if err == nil {
		t.Fatal("expected error deleting non-existent key")
	}
	if !IsNotFound(err) {
		t.Fatalf("expected ErrSecretNotFound, got %T", err)
	}
}

func TestEncryptedFileProvider_FilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.enc")

	pass := func() (string, error) { return "pass", nil }
	p := NewEncryptedFileProvider(path, pass)

	if err := p.Set("KEY", "val"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Fatalf("expected file permissions 0600, got %04o", perm)
	}
}

func TestEncryptedFileProvider_NotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.enc")

	pass := func() (string, error) { return "pass", nil }
	p := NewEncryptedFileProvider(path, pass)

	_, err := p.Get("MISSING")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
	if !IsNotFound(err) {
		t.Fatalf("expected ErrSecretNotFound, got %T: %v", err, err)
	}
}

func TestEncryptedFileProvider_SetBatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.enc")

	pass := func() (string, error) { return "batch-pass", nil }
	p := NewEncryptedFileProvider(path, pass)

	// Batch set multiple keys
	pairs := map[string]string{
		"OPENAI_API_KEY":    "sk-123",
		"ANTHROPIC_API_KEY": "ant-456",
		"GEMINI_API_KEY":    "gem-789",
	}
	if err := p.SetBatch(pairs); err != nil {
		t.Fatalf("SetBatch: %v", err)
	}

	// Verify all keys
	for k, expected := range pairs {
		val, err := p.Get(k)
		if err != nil {
			t.Fatalf("Get(%q): %v", k, err)
		}
		if val != expected {
			t.Fatalf("Get(%q) = %q, want %q", k, val, expected)
		}
	}

	// Verify re-open round-trip
	p2 := NewEncryptedFileProvider(path, pass)
	for k, expected := range pairs {
		val, err := p2.Get(k)
		if err != nil {
			t.Fatalf("Get(%q) after re-open: %v", k, err)
		}
		if val != expected {
			t.Fatalf("Get(%q) after re-open = %q, want %q", k, val, expected)
		}
	}

	keys, err := p2.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(keys))
	}
}

func TestEncryptedFileProvider_SetBatchMerge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.enc")

	pass := func() (string, error) { return "merge-pass", nil }
	p := NewEncryptedFileProvider(path, pass)

	// Set initial key
	if err := p.Set("EXISTING", "old-value"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Batch set — should merge, not replace
	if err := p.SetBatch(map[string]string{
		"NEW_KEY":  "new-value",
		"EXISTING": "updated-value",
	}); err != nil {
		t.Fatalf("SetBatch: %v", err)
	}

	val, err := p.Get("EXISTING")
	if err != nil {
		t.Fatalf("Get EXISTING: %v", err)
	}
	if val != "updated-value" {
		t.Fatalf("expected updated-value, got %q", val)
	}

	val, err = p.Get("NEW_KEY")
	if err != nil {
		t.Fatalf("Get NEW_KEY: %v", err)
	}
	if val != "new-value" {
		t.Fatalf("expected new-value, got %q", val)
	}
}

func TestEncryptedFileProvider_Name(t *testing.T) {
	p := NewEncryptedFileProvider("", nil)
	if p.Name() != "encrypted-file" {
		t.Fatalf("expected 'encrypted-file', got %q", p.Name())
	}
}
