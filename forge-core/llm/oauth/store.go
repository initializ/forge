package oauth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/initializ/forge/forge-core/secrets"
)

// credentialsDirOverride is set by SetCredentialsDir. Empty means
// fall back to the home-directory default. Set at process start by
// callers that need a non-default location (e.g., K8s pod with the
// token Secret mounted somewhere other than ~/.forge/credentials).
//
// Not concurrency-protected — expected to be set once at startup
// before any goroutines call Save/LoadCredentials. Review B11.
var credentialsDirOverride string

// SetCredentialsDir overrides the default OAuth credentials
// directory. Intended for early-startup wiring; calling it after
// concurrent Save/Load is in flight is a data race.
//
// Pass "" to clear the override and revert to the home-based
// default. Review B11.
func SetCredentialsDir(dir string) { credentialsDirOverride = dir }

// DefaultCredentialsDir returns the directory used by the
// encrypted/plaintext credential helpers. If SetCredentialsDir
// has been called with a non-empty value, that wins; otherwise
// returns ~/.forge/credentials.
func DefaultCredentialsDir() (string, error) {
	if credentialsDirOverride != "" {
		return credentialsDirOverride, nil
	}
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

// --- generic JSON records (beyond Token) ---
//
// SaveRecord / LoadRecord / DeleteRecord persist an arbitrary
// JSON-serializable value under a caller-supplied key, using the same
// encrypted-preferred, plaintext-fallback strategy as the token
// helpers. Added for the MCP OAuth discovery/registration record
// (#316): the minted client_id + discovered endpoints must survive
// across a refresh and a pod restart, exactly like the token itself.

// SaveRecord persists v (marshaled to JSON) under key.
func SaveRecord(key string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshaling record: %w", err)
	}
	if ep := encryptedProvider(); ep != nil {
		if err := ep.Set(key, string(data)); err != nil {
			return fmt.Errorf("saving encrypted record: %w", err)
		}
		_ = removeRecordFile(key) // migration clean-up
		return nil
	}
	return saveRecordPlaintext(key, data)
}

// LoadRecord loads a record saved by SaveRecord into v. Returns
// found=false (nil error) when no record exists for key.
func LoadRecord(key string, v any) (found bool, err error) {
	if ep := encryptedProvider(); ep != nil {
		val, gErr := ep.Get(key)
		if gErr == nil {
			if jErr := json.Unmarshal([]byte(val), v); jErr != nil {
				return false, fmt.Errorf("parsing encrypted record: %w", jErr)
			}
			return true, nil
		}
		if !secrets.IsNotFound(gErr) {
			return false, fmt.Errorf("reading encrypted record: %w", gErr)
		}
		// Not found in encrypted store — fall through to plaintext.
	}
	return loadRecordPlaintext(key, v)
}

// DeleteRecord removes a record from both stores. Idempotent.
func DeleteRecord(key string) error {
	if ep := encryptedProvider(); ep != nil {
		if err := ep.Delete(key); err != nil && !secrets.IsNotFound(err) {
			return fmt.Errorf("deleting encrypted record: %w", err)
		}
	}
	return removeRecordFile(key)
}

func recordPath(key string) (string, error) {
	dir, err := DefaultCredentialsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, key+".json"), nil
}

func saveRecordPlaintext(key string, data []byte) error {
	dir, err := DefaultCredentialsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating credentials directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, key+".json"), data, 0o600); err != nil {
		return fmt.Errorf("writing record: %w", err)
	}
	return nil
}

func loadRecordPlaintext(key string, v any) (bool, error) {
	path, err := recordPath(key)
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("reading record: %w", err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, fmt.Errorf("parsing record: %w", err)
	}
	return true, nil
}

func removeRecordFile(key string) error {
	path, err := recordPath(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing record: %w", err)
	}
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
