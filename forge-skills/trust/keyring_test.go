package trust

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestKeyring_AddAndGet(t *testing.T) {
	kr := NewKeyring()
	pub, _, _ := GenerateKeyPair()

	kr.Add("test-key", pub)

	got := kr.Get("test-key")
	if got == nil {
		t.Fatal("key not found")
	}
	if !got.Equal(pub) {
		t.Fatal("key mismatch")
	}
}

func TestKeyring_List(t *testing.T) {
	kr := NewKeyring()
	pub1, _, _ := GenerateKeyPair()
	pub2, _, _ := GenerateKeyPair()

	kr.Add("key1", pub1)
	kr.Add("key2", pub2)

	ids := kr.List()
	if len(ids) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(ids))
	}
}

func TestKeyring_Verify(t *testing.T) {
	kr := NewKeyring()
	pub, priv, _ := GenerateKeyPair()
	kr.Add("signer", pub)

	content := []byte("signed content")
	sig, _ := SignSkill(content, priv)

	keyID, ok := kr.Verify(content, sig)
	if !ok {
		t.Fatal("keyring verification failed")
	}
	if keyID != "signer" {
		t.Fatalf("expected keyID 'signer', got %q", keyID)
	}
}

func TestKeyring_VerifyNoMatch(t *testing.T) {
	kr := NewKeyring()
	pub1, _, _ := GenerateKeyPair()
	kr.Add("key1", pub1)

	_, priv2, _ := GenerateKeyPair()
	content := []byte("content")
	sig, _ := SignSkill(content, priv2)

	_, ok := kr.Verify(content, sig)
	if ok {
		t.Fatal("keyring should not verify with wrong key")
	}
}

func TestKeyring_LoadFromDir(t *testing.T) {
	dir := t.TempDir()

	// Create a valid key file
	pub, _, _ := GenerateKeyPair()
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	if err := os.WriteFile(filepath.Join(dir, "test.pub"), []byte(pubB64), 0644); err != nil {
		t.Fatal(err)
	}

	kr := NewKeyring()
	if err := kr.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir failed: %v", err)
	}

	got := kr.Get("test")
	if got == nil {
		t.Fatal("key not loaded")
	}
	if len(got) != ed25519.PublicKeySize {
		t.Fatalf("loaded key wrong size: %d", len(got))
	}
}

func TestKeyring_LoadFromDir_NonExistent(t *testing.T) {
	kr := NewKeyring()
	err := kr.LoadFromDir("/nonexistent/path")
	if err != nil {
		t.Fatalf("should not error on missing directory: %v", err)
	}
}

func TestKeyring_LoadFromDir_InvalidKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.pub"), []byte("not-base64!!!"), 0644); err != nil {
		t.Fatal(err)
	}

	kr := NewKeyring()
	err := kr.LoadFromDir(dir)
	if err == nil {
		t.Fatal("expected error for invalid key")
	}
}
