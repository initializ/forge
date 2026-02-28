package secrets

// ChainProvider tries multiple providers in order, returning the first successful result.
type ChainProvider struct {
	providers []Provider
}

// NewChainProvider creates a ChainProvider that queries providers in order.
// The first provider to return a value wins. Non-NotFound errors (e.g. decrypt
// failure) are propagated immediately.
func NewChainProvider(providers ...Provider) *ChainProvider {
	return &ChainProvider{providers: providers}
}

func (c *ChainProvider) Name() string { return "chain" }

// Get tries each provider in order. Returns the first successful value.
// Non-NotFound errors are propagated immediately.
func (c *ChainProvider) Get(key string) (string, error) {
	for _, p := range c.providers {
		val, err := p.Get(key)
		if err == nil {
			return val, nil
		}
		if !IsNotFound(err) {
			return "", err // propagate real errors (e.g. decryption failure)
		}
	}
	return "", &ErrSecretNotFound{Key: key, Provider: c.Name()}
}

// GetWithSource tries each provider in order and returns the value along with
// the name of the provider that resolved it.
func (c *ChainProvider) GetWithSource(key string) (value, source string, err error) {
	for _, p := range c.providers {
		val, err := p.Get(key)
		if err == nil {
			return val, p.Name(), nil
		}
		if !IsNotFound(err) {
			return "", "", err
		}
	}
	return "", "", &ErrSecretNotFound{Key: key, Provider: c.Name()}
}

// List returns the union of all keys across all providers, deduplicated.
func (c *ChainProvider) List() ([]string, error) {
	seen := make(map[string]bool)
	var all []string

	for _, p := range c.providers {
		keys, err := p.List()
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			if !seen[k] {
				seen[k] = true
				all = append(all, k)
			}
		}
	}
	return all, nil
}
