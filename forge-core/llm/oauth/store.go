package oauth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/initializ/forge/forge-core/secrets"
)

// DefaultCredentialsDir returns the default directory for OAuth credentials.
func DefaultCredentialsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting home directory: %w", err)
	}
	return filepath.Join(home, ".forge", "credentials"), nil
}

// oauthSecretKey returns the encrypted-store key for a provider's OAuth token.
func oauthSecretKey(provider string) string {
	return "OAUTH_TOKEN_" + strings.ToUpper(provider)
}

// encryptedProvider returns an EncryptedFileProvider if FORGE_PASSPHRASE is set,
// or nil when encryption is unavailable.
func encryptedProvider() *secrets.EncryptedFileProvider {
	pass := os.Getenv("FORGE_PASSPHRASE")
	if pass == "" {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return secrets.NewEncryptedFileProvider(
		filepath.Join(home, ".forge", "secrets.enc"),
		func() (string, error) { return pass, nil },
	)
}

// SaveCredentials stores OAuth token data. When FORGE_PASSPHRASE is available
// the token is saved to the encrypted secrets file and any plaintext file is
// removed. Otherwise it falls back to writing a plaintext JSON file.
func SaveCredentials(provider string, token *Token) error {
	if ep := encryptedProvider(); ep != nil {
		data, err := json.Marshal(token)
		if err != nil {
			return fmt.Errorf("marshaling token: %w", err)
		}
		if err := ep.Set(oauthSecretKey(provider), string(data)); err != nil {
			return fmt.Errorf("saving encrypted token: %w", err)
		}
		// Remove plaintext file if it exists (migration clean-up).
		_ = removePlaintextFile(provider)
		return nil
	}

	return savePlaintext(provider, token)
}

// LoadCredentials loads OAuth token data. It tries the encrypted store first,
// then falls back to the plaintext file so that pre-migration credentials
// continue to work.
func LoadCredentials(provider string) (*Token, error) {
	if ep := encryptedProvider(); ep != nil {
		val, err := ep.Get(oauthSecretKey(provider))
		if err == nil {
			var token Token
			if jErr := json.Unmarshal([]byte(val), &token); jErr != nil {
				return nil, fmt.Errorf("parsing encrypted token: %w", jErr)
			}
			return &token, nil
		}
		// Key not found in encrypted store — fall through to plaintext.
		if !secrets.IsNotFound(err) {
			return nil, fmt.Errorf("reading encrypted token: %w", err)
		}
	}

	return loadPlaintext(provider)
}

// DeleteCredentials removes stored OAuth credentials from both the encrypted
// store and the plaintext file.
func DeleteCredentials(provider string) error {
	if ep := encryptedProvider(); ep != nil {
		err := ep.Delete(oauthSecretKey(provider))
		if err != nil && !secrets.IsNotFound(err) {
			return fmt.Errorf("deleting encrypted token: %w", err)
		}
	}

	return removePlaintextFile(provider)
}

// MigrateToEncrypted moves a provider's plaintext credentials into the
// encrypted store. It is a no-op if no plaintext file exists or the encrypted
// provider is unavailable.
func MigrateToEncrypted(provider string) error {
	ep := encryptedProvider()
	if ep == nil {
		return nil
	}

	token, err := loadPlaintext(provider)
	if err != nil {
		return err
	}
	if token == nil {
		return nil // nothing to migrate
	}

	data, err := json.Marshal(token)
	if err != nil {
		return fmt.Errorf("marshaling token for migration: %w", err)
	}

	if err := ep.Set(oauthSecretKey(provider), string(data)); err != nil {
		return fmt.Errorf("saving migrated token: %w", err)
	}

	_ = removePlaintextFile(provider)
	return nil
}

// --- plaintext helpers ---

func savePlaintext(provider string, token *Token) error {
	dir, err := DefaultCredentialsDir()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating credentials directory: %w", err)
	}

	data, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling token: %w", err)
	}

	path := filepath.Join(dir, provider+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing credentials: %w", err)
	}

	return nil
}

func loadPlaintext(provider string) (*Token, error) {
	dir, err := DefaultCredentialsDir()
	if err != nil {
		return nil, err
	}

	path := filepath.Join(dir, provider+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no credentials stored
		}
		return nil, fmt.Errorf("reading credentials: %w", err)
	}

	var token Token
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, fmt.Errorf("parsing credentials: %w", err)
	}

	return &token, nil
}

func removePlaintextFile(provider string) error {
	dir, err := DefaultCredentialsDir()
	if err != nil {
		return err
	}

	path := filepath.Join(dir, provider+".json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing credentials: %w", err)
	}

	return nil
}
