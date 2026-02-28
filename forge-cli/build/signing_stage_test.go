package build

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-core/types"
	"github.com/initializ/forge/forge-skills/trust"
)

func TestSigningStage_UnsignedChecksums(t *testing.T) {
	dir := t.TempDir()

	// Create a test file
	testContent := []byte("hello world")
	testFile := filepath.Join(dir, "agent.json")
	_ = os.WriteFile(testFile, testContent, 0644)

	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: dir})
	bc.Config = &types.ForgeConfig{}
	bc.GeneratedFiles["agent.json"] = testFile

	stage := &SigningStage{}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Verify checksums.json was created
	data, err := os.ReadFile(filepath.Join(dir, "checksums.json"))
	if err != nil {
		t.Fatalf("reading checksums.json: %v", err)
	}

	var cf ChecksumsFile
	if err := json.Unmarshal(data, &cf); err != nil {
		t.Fatalf("parsing checksums.json: %v", err)
	}

	if cf.Version != "1" {
		t.Fatalf("expected version 1, got %q", cf.Version)
	}
	if cf.Signature != "" {
		t.Fatal("expected empty signature for unsigned checksums")
	}

	// Verify checksum value
	h := sha256.Sum256(testContent)
	expected := hex.EncodeToString(h[:])
	if cf.Checksums["agent.json"] != expected {
		t.Fatalf("expected checksum %s, got %s", expected, cf.Checksums["agent.json"])
	}
}

func TestSigningStage_SignedChecksums(t *testing.T) {
	dir := t.TempDir()

	// Create test file
	testContent := []byte("signed content")
	testFile := filepath.Join(dir, "agent.json")
	_ = os.WriteFile(testFile, testContent, 0644)

	// Generate keypair and write private key
	pub, priv, err := trust.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	keyPath := filepath.Join(dir, "test-key.pem")
	_ = os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(priv)+"\n"), 0600)

	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{
		OutputDir:      dir,
		SigningKeyPath: keyPath,
	})
	bc.Config = &types.ForgeConfig{}
	bc.GeneratedFiles["agent.json"] = testFile

	stage := &SigningStage{}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Parse checksums.json
	data, err := os.ReadFile(filepath.Join(dir, "checksums.json"))
	if err != nil {
		t.Fatalf("reading checksums.json: %v", err)
	}

	var cf ChecksumsFile
	if err := json.Unmarshal(data, &cf); err != nil {
		t.Fatalf("parsing checksums.json: %v", err)
	}

	if cf.Signature == "" {
		t.Fatal("expected non-empty signature")
	}
	if cf.KeyID != "test-key" {
		t.Fatalf("expected key ID 'test-key', got %q", cf.KeyID)
	}

	// Verify signature
	sig, err := base64.StdEncoding.DecodeString(cf.Signature)
	if err != nil {
		t.Fatalf("decoding signature: %v", err)
	}

	checksumData, _ := json.Marshal(cf.Checksums)
	if !trust.Verify(checksumData, sig, pub) {
		t.Fatal("signature verification failed")
	}
}

func TestSigningStage_TamperedFileDetected(t *testing.T) {
	dir := t.TempDir()

	testFile := filepath.Join(dir, "agent.json")
	_ = os.WriteFile(testFile, []byte("original"), 0644)

	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{OutputDir: dir})
	bc.Config = &types.ForgeConfig{}
	bc.GeneratedFiles["agent.json"] = testFile

	stage := &SigningStage{}
	if err := stage.Execute(context.Background(), bc); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Parse checksums
	data, _ := os.ReadFile(filepath.Join(dir, "checksums.json"))
	var cf ChecksumsFile
	if err := json.Unmarshal(data, &cf); err != nil {
		t.Fatalf("parsing checksums: %v", err)
	}

	// Tamper with the file
	_ = os.WriteFile(testFile, []byte("tampered"), 0644)

	// Verify checksum mismatch
	tamperedData, _ := os.ReadFile(testFile)
	h := sha256.Sum256(tamperedData)
	tamperedChecksum := hex.EncodeToString(h[:])

	if cf.Checksums["agent.json"] == tamperedChecksum {
		t.Fatal("expected checksum mismatch after tampering")
	}
}

func TestLoadPrivateKey(t *testing.T) {
	dir := t.TempDir()

	_, priv, _ := ed25519.GenerateKey(nil)
	keyPath := filepath.Join(dir, "my-key.pem")
	_ = os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(priv)+"\n"), 0600)

	loaded, keyID, err := loadPrivateKey(keyPath)
	if err != nil {
		t.Fatalf("loadPrivateKey: %v", err)
	}
	if keyID != "my-key" {
		t.Fatalf("expected key ID 'my-key', got %q", keyID)
	}
	if len(loaded) != ed25519.PrivateKeySize {
		t.Fatalf("expected key size %d, got %d", ed25519.PrivateKeySize, len(loaded))
	}
}
