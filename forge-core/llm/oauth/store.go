package oauth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// DefaultCredentialsDir returns the default directory for OAuth credentials.
func DefaultCredentialsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting home directory: %w", err)
	}
	return filepath.Join(home, ".forge", "credentials"), nil
}

// SaveCredentials stores OAuth token data to disk with restricted permissions.
func SaveCredentials(provider string, token *Token) error {
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

// LoadCredentials loads OAuth token data from disk.
func LoadCredentials(provider string) (*Token, error) {
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

// DeleteCredentials removes stored OAuth credentials for a provider.
func DeleteCredentials(provider string) error {
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
