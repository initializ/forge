// Package secrets provides a provider-based secret management system.
package secrets

import (
	"errors"
	"fmt"
)

// Provider is the interface that secret backends must implement.
type Provider interface {
	// Get retrieves a secret by key. Returns ErrSecretNotFound if the key does not exist.
	Get(key string) (string, error)

	// List returns all available secret keys.
	List() ([]string, error)

	// Name returns the provider's identifier (e.g. "env", "encrypted-file").
	Name() string
}

// ErrSecretNotFound is returned when a requested secret key does not exist.
type ErrSecretNotFound struct {
	Key      string
	Provider string
}

func (e *ErrSecretNotFound) Error() string {
	return fmt.Sprintf("secret %q not found in provider %q", e.Key, e.Provider)
}

// IsNotFound reports whether err is an ErrSecretNotFound.
func IsNotFound(err error) bool {
	var target *ErrSecretNotFound
	return errors.As(err, &target)
}
