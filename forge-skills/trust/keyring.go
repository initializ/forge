package trust

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Keyring manages a set of trusted Ed25519 public keys.
type Keyring struct {
	keys map[string]ed25519.PublicKey // keyID -> pubkey
}

// NewKeyring creates an empty keyring.
func NewKeyring() *Keyring {
	return &Keyring{keys: make(map[string]ed25519.PublicKey)}
}

// Add registers a public key with the given ID.
func (k *Keyring) Add(keyID string, pubKey ed25519.PublicKey) {
	k.keys[keyID] = pubKey
}

// Get returns the public key for the given ID, or nil if not found.
func (k *Keyring) Get(keyID string) ed25519.PublicKey {
	return k.keys[keyID]
}

// List returns all key IDs in the keyring.
func (k *Keyring) List() []string {
	ids := make([]string, 0, len(k.keys))
	for id := range k.keys {
		ids = append(ids, id)
	}
	return ids
}

// LoadFromDir reads all *.pub files from a directory and adds them to the keyring.
// Each file should contain a base64-encoded Ed25519 public key.
// The key ID is derived from the filename (without .pub extension).
func (k *Keyring) LoadFromDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // directory doesn't exist yet, that's fine
		}
		return fmt.Errorf("reading key directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".pub") {
			continue
		}

		keyID := strings.TrimSuffix(entry.Name(), ".pub")
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return fmt.Errorf("reading key %q: %w", keyID, err)
		}

		// Decode base64
		pubBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
		if err != nil {
			return fmt.Errorf("decoding key %q: %w", keyID, err)
		}

		if len(pubBytes) != ed25519.PublicKeySize {
			return fmt.Errorf("key %q has invalid size: %d (expected %d)", keyID, len(pubBytes), ed25519.PublicKeySize)
		}

		k.keys[keyID] = ed25519.PublicKey(pubBytes)
	}

	return nil
}

// Verify tries all keys in the keyring against the content and signature.
// Returns the matching key ID and true if verified, or empty string and false.
func (k *Keyring) Verify(content, signature []byte) (keyID string, ok bool) {
	for id, pubKey := range k.keys {
		if VerifySkill(content, signature, pubKey) {
			return id, true
		}
	}
	return "", false
}

// DefaultKeyring loads trusted keys from ~/.forge/trusted-keys/.
func DefaultKeyring() *Keyring {
	kr := NewKeyring()
	home, err := os.UserHomeDir()
	if err != nil {
		return kr
	}
	_ = kr.LoadFromDir(filepath.Join(home, ".forge", "trusted-keys"))
	return kr
}
