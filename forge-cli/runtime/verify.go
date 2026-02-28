package runtime

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/initializ/forge/forge-skills/trust"
)

// ChecksumsFile mirrors the JSON structure written by the signing stage.
type ChecksumsFile struct {
	Version   string            `json:"version"`
	Checksums map[string]string `json:"checksums"`
	Timestamp string            `json:"timestamp"`
	Signature string            `json:"signature,omitempty"`
	KeyID     string            `json:"key_id,omitempty"`
}

// VerifyBuildOutput verifies the integrity of build output files against checksums.json.
// Returns nil if checksums.json is not found (verification is optional).
// Returns an error if any file's checksum doesn't match or if the signature is invalid.
func VerifyBuildOutput(outputDir string) error {
	checksumPath := filepath.Join(outputDir, "checksums.json")

	data, err := os.ReadFile(checksumPath)
	if os.IsNotExist(err) {
		return nil // checksums.json is optional
	}
	if err != nil {
		return fmt.Errorf("reading checksums.json: %w", err)
	}

	var cf ChecksumsFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return fmt.Errorf("parsing checksums.json: %w", err)
	}

	// Verify each file's checksum.
	for rel, expectedHash := range cf.Checksums {
		absPath := filepath.Join(outputDir, rel)
		fileData, err := os.ReadFile(absPath)
		if err != nil {
			return fmt.Errorf("reading %s: %w", rel, err)
		}

		h := sha256.Sum256(fileData)
		actual := hex.EncodeToString(h[:])
		if actual != expectedHash {
			return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", rel, expectedHash, actual)
		}
	}

	// Verify signature if present.
	if cf.Signature != "" {
		sig, err := base64.StdEncoding.DecodeString(cf.Signature)
		if err != nil {
			return fmt.Errorf("decoding signature: %w", err)
		}

		checksumData, err := json.Marshal(cf.Checksums)
		if err != nil {
			return fmt.Errorf("marshalling checksums for verification: %w", err)
		}

		kr := trust.DefaultKeyring()
		keyID, ok := kr.Verify(checksumData, sig)
		if !ok {
			return fmt.Errorf("signature verification failed: no trusted key matched (key_id: %s)", cf.KeyID)
		}
		_ = keyID // signature is valid
	}

	return nil
}
