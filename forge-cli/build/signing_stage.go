package build

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-skills/trust"
)

// SigningStage computes SHA-256 checksums of all generated files and optionally
// signs them with an Ed25519 private key. This stage should run last in the pipeline.
type SigningStage struct{}

func (s *SigningStage) Name() string { return "signing" }

// ChecksumsFile is the JSON structure written to checksums.json.
type ChecksumsFile struct {
	Version   string            `json:"version"`
	Checksums map[string]string `json:"checksums"` // relPath -> sha256 hex
	Timestamp string            `json:"timestamp"`
	Signature string            `json:"signature,omitempty"` // base64 Ed25519 signature of checksums JSON
	KeyID     string            `json:"key_id,omitempty"`    // identifier of the signing key
}

func (s *SigningStage) Execute(ctx context.Context, bc *pipeline.BuildContext) error {
	// Compute SHA-256 checksums for all generated files.
	checksums := make(map[string]string)

	// Sort keys for deterministic output.
	relPaths := make([]string, 0, len(bc.GeneratedFiles))
	for rel := range bc.GeneratedFiles {
		relPaths = append(relPaths, rel)
	}
	sort.Strings(relPaths)

	for _, rel := range relPaths {
		absPath := bc.GeneratedFiles[rel]
		data, err := os.ReadFile(absPath)
		if err != nil {
			return fmt.Errorf("reading %s for checksum: %w", rel, err)
		}
		h := sha256.Sum256(data)
		checksums[rel] = hex.EncodeToString(h[:])
	}

	cf := ChecksumsFile{
		Version:   "1",
		Checksums: checksums,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	// Sign if a signing key is available.
	signingKeyPath := bc.Opts.SigningKeyPath
	if signingKeyPath == "" {
		// Check default location.
		home, err := os.UserHomeDir()
		if err == nil {
			defaultPath := filepath.Join(home, ".forge", "signing-key.pem")
			if _, err := os.Stat(defaultPath); err == nil {
				signingKeyPath = defaultPath
			}
		}
	}

	if signingKeyPath != "" {
		privKey, keyID, err := loadPrivateKey(signingKeyPath)
		if err != nil {
			return fmt.Errorf("loading signing key: %w", err)
		}

		// Sign the checksums map (deterministic JSON).
		checksumData, err := json.Marshal(cf.Checksums)
		if err != nil {
			return fmt.Errorf("marshalling checksums for signing: %w", err)
		}

		sig := trust.Sign(checksumData, privKey)
		cf.Signature = base64.StdEncoding.EncodeToString(sig)
		cf.KeyID = keyID
	}

	data, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling checksums.json: %w", err)
	}

	outPath := filepath.Join(bc.Opts.OutputDir, "checksums.json")
	if err := os.WriteFile(outPath, data, 0644); err != nil {
		return fmt.Errorf("writing checksums.json: %w", err)
	}

	bc.AddFile("checksums.json", outPath)
	return nil
}

// loadPrivateKey reads a base64-encoded Ed25519 private key from a file.
// Returns the private key and a key ID derived from the filename.
func loadPrivateKey(path string) (ed25519.PrivateKey, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("reading key file: %w", err)
	}

	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, "", fmt.Errorf("decoding key: %w", err)
	}

	if len(raw) != ed25519.PrivateKeySize {
		return nil, "", fmt.Errorf("invalid private key size: %d (expected %d)", len(raw), ed25519.PrivateKeySize)
	}

	keyID := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	return ed25519.PrivateKey(raw), keyID, nil
}
