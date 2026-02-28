package secrets

import "os"

// EnvProvider reads secrets from environment variables.
type EnvProvider struct {
	// prefix is an optional prefix applied to key lookups (e.g. "FORGE_").
	prefix string
}

// NewEnvProvider creates an EnvProvider. An optional prefix is prepended
// to every key before looking it up in the environment.
func NewEnvProvider(prefix string) *EnvProvider {
	return &EnvProvider{prefix: prefix}
}

func (p *EnvProvider) Name() string { return "env" }

// Get returns the environment variable value for key (with optional prefix).
func (p *EnvProvider) Get(key string) (string, error) {
	v := os.Getenv(p.prefix + key)
	if v == "" {
		return "", &ErrSecretNotFound{Key: key, Provider: p.Name()}
	}
	return v, nil
}

// List returns nil — environment variables are not enumerable by design.
func (p *EnvProvider) List() ([]string, error) {
	return nil, nil
}
