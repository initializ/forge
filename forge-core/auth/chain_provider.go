package auth

import (
	"context"
	"errors"
)

// ChainProvider composes multiple Providers into a single Provider, calling
// them in order and returning the first non-yielding result.
//
// Behavior:
//   - A Provider returning (id, nil) wins; later providers are not consulted.
//   - A Provider returning ErrTokenNotForMe is skipped; the chain advances.
//   - Any other error stops the chain immediately (fail-closed). The middleware
//     surfaces this as 401. Falling through on non-yield errors would let an
//     attacker bypass a temporarily-misbehaving provider.
//
// A ChainProvider is immutable after construction and safe for concurrent use.
type ChainProvider struct {
	providers []Provider
}

// NewChainProvider returns a chain that calls the given providers in order.
// A chain with zero providers verifies nothing — Verify returns ErrTokenNotForMe.
func NewChainProvider(providers ...Provider) *ChainProvider {
	// Defensive copy so callers can't mutate the slice after construction.
	cp := make([]Provider, len(providers))
	copy(cp, providers)
	return &ChainProvider{providers: cp}
}

// Name implements Provider.
func (c *ChainProvider) Name() string { return "chain" }

// Providers returns a defensive copy of the configured providers, in order.
func (c *ChainProvider) Providers() []Provider {
	out := make([]Provider, len(c.providers))
	copy(out, c.providers)
	return out
}

// Verify implements Provider with first-match-wins semantics.
func (c *ChainProvider) Verify(ctx context.Context, token string, headers Headers) (*Identity, error) {
	for _, p := range c.providers {
		id, err := p.Verify(ctx, token, headers)
		if err == nil {
			return id, nil
		}
		if errors.Is(err, ErrTokenNotForMe) {
			continue
		}
		// Fail closed: non-yield error stops the chain.
		return nil, err
	}
	return nil, ErrTokenNotForMe
}

// PrependChain returns a new ChainProvider whose providers are `prepend`
// followed by the providers of `chain`. If chain is a *ChainProvider, its
// providers are flattened (no nested chains). If chain is a non-chain
// Provider, it is appended as a single element. If chain is nil, the result
// contains only the prepended providers.
//
// Useful for the runner to inject a loopback static_token at the chain head
// without callers having to know whether their chain already exists.
func PrependChain(chain Provider, prepend ...Provider) *ChainProvider {
	var existing []Provider
	switch v := chain.(type) {
	case nil:
		// nothing to extend
	case *ChainProvider:
		existing = v.providers
	default:
		existing = []Provider{v}
	}
	combined := make([]Provider, 0, len(prepend)+len(existing))
	combined = append(combined, prepend...)
	combined = append(combined, existing...)
	return &ChainProvider{providers: combined}
}
