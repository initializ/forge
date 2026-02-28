package runtime

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-skills/trust"
)

func TestVerifyBuildOutput_NoChecksums(t *testing.T) {
	dir := t.TempDir()
	if err := VerifyBuildOutput(dir); err != nil {
		t.Fatalf("expected nil for missing checksums.json, got: %v", err)
	}
}

func TestVerifyBuildOutput_ValidChecksums(t *testing.T) {
	dir := t.TempDir()

	// Create test file
	content := []byte("test content")
	_ = os.WriteFile(filepath.Join(dir, "agent.json"), content, 0644)

	h := sha256.Sum256(content)
	cf := ChecksumsFile{
		Version:   "1",
		Checksums: map[string]string{"agent.json": hex.EncodeToString(h[:])},
		Timestamp: "2025-01-01T00:00:00Z",
	}

	data, _ := json.Marshal(cf)
	_ = os.WriteFile(filepath.Join(dir, "checksums.json"), data, 0644)

	if err := VerifyBuildOutput(dir); err != nil {
		t.Fatalf("expected valid checksums to pass, got: %v", err)
	}
}

func TestVerifyBuildOutput_TamperedFile(t *testing.T) {
	dir := t.TempDir()

	original := []byte("original")
	_ = os.WriteFile(filepath.Join(dir, "agent.json"), original, 0644)

	h := sha256.Sum256(original)
	cf := ChecksumsFile{
		Version:   "1",
		Checksums: map[string]string{"agent.json": hex.EncodeToString(h[:])},
	}

	data, _ := json.Marshal(cf)
	_ = os.WriteFile(filepath.Join(dir, "checksums.json"), data, 0644)

	// Tamper with the file
	_ = os.WriteFile(filepath.Join(dir, "agent.json"), []byte("tampered"), 0644)

	err := VerifyBuildOutput(dir)
	if err == nil {
		t.Fatal("expected error for tampered file")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch error, got: %v", err)
	}
}

func TestVerifyBuildOutput_ValidSignature(t *testing.T) {
	dir := t.TempDir()

	content := []byte("signed content")
	_ = os.WriteFile(filepath.Join(dir, "agent.json"), content, 0644)

	h := sha256.Sum256(content)
	checksums := map[string]string{"agent.json": hex.EncodeToString(h[:])}

	pub, priv, _ := trust.GenerateKeyPair()

	checksumData, _ := json.Marshal(checksums)
	sig := trust.Sign(checksumData, priv)

	// Write public key to trusted keys dir
	home, _ := os.UserHomeDir()
	trustDir := filepath.Join(home, ".forge", "trusted-keys")
	_ = os.MkdirAll(trustDir, 0700)
	pubPath := filepath.Join(trustDir, "test-verify.pub")
	_ = os.WriteFile(pubPath, []byte(base64.StdEncoding.EncodeToString(pub)), 0644)
	defer func() { _ = os.Remove(pubPath) }()

	cf := ChecksumsFile{
		Version:   "1",
		Checksums: checksums,
		Signature: base64.StdEncoding.EncodeToString(sig),
		KeyID:     "test-verify",
	}

	data, _ := json.Marshal(cf)
	_ = os.WriteFile(filepath.Join(dir, "checksums.json"), data, 0644)

	if err := VerifyBuildOutput(dir); err != nil {
		t.Fatalf("expected valid signature to pass, got: %v", err)
	}
}

func TestVerifyBuildOutput_InvalidSignature(t *testing.T) {
	dir := t.TempDir()

	content := []byte("content")
	_ = os.WriteFile(filepath.Join(dir, "agent.json"), content, 0644)

	h := sha256.Sum256(content)
	checksums := map[string]string{"agent.json": hex.EncodeToString(h[:])}

	cf := ChecksumsFile{
		Version:   "1",
		Checksums: checksums,
		Signature: base64.StdEncoding.EncodeToString([]byte("invalid-signature-that-is-exactly-64-bytes-long-for-ed25519!!")),
		KeyID:     "unknown-key",
	}

	data, _ := json.Marshal(cf)
	_ = os.WriteFile(filepath.Join(dir, "checksums.json"), data, 0644)

	err := VerifyBuildOutput(dir)
	if err == nil {
		t.Fatal("expected error for invalid signature")
	}
	if !strings.Contains(err.Error(), "signature verification failed") {
		t.Fatalf("expected signature verification error, got: %v", err)
	}
}
