// Package statictoken implements a Provider that matches the presented
// bearer token against a single expected value using constant-time
// comparison.
//
// Two intended uses:
//
//  1. Channel adapter loopback. The runner generates a random per-process
//     token, configures a statictoken Provider with it (placed at the head
//     of the chain), and shares the same token with Slack/Telegram adapters
//     so their callbacks into the local A2A server authenticate cheaply
//     without touching an upstream IdP.
//
//  2. Local dev / CI. A fixed token configured via env var lets developers
//     hit a running agent with `curl -H "Authorization: Bearer $FORGE_DEV_TOKEN"`
//     without setting up an IdP.
//
// Mismatch returns ErrTokenNotForMe (yield to next provider), not
// ErrTokenRejected — the loopback token is "not for me" from the
// perspective of an external client, and chain semantics require yielding
// in that case.
package statictoken

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"maps"
	"os"
	"slices"

	"github.com/initializ/forge/forge-core/auth"
)

// ProviderName is the type name used to register and reference this provider.
const ProviderName = "static_token"

// Config controls the static_token provider.
type Config struct {
	// Token is the expected bearer value (literal). Prefer TokenEnv for
	// non-test use — putting a secret in YAML is a footgun.
	Token string `yaml:"token,omitempty"`

	// TokenEnv names an environment variable that holds the token at
	// construction time. Read once in New(); subsequent env changes do
	// not affect the running provider.
	TokenEnv string `yaml:"token_env,omitempty"`

	// Identity is returned on a successful match (a defensive copy is
	// returned to callers). If Source is empty it defaults to "static_token".
	Identity auth.Identity `yaml:"identity,omitempty"`
}

// Validate returns ErrProviderNotConfigured when neither Token nor TokenEnv
// resolves to a non-empty value.
func (c Config) Validate() error {
	if c.Token == "" && c.TokenEnv == "" {
		return fmt.Errorf("%w: token or token_env required", auth.ErrProviderNotConfigured)
	}
	return nil
}

// Provider implements auth.Provider with a constant-time token compare.
type Provider struct {
	expected []byte
	identity auth.Identity
}

// New constructs a Provider after resolving the token (TokenEnv takes
// precedence over Token literal). Returns ErrProviderNotConfigured if the
// resolved token is empty.
func New(cfg Config) (*Provider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	token := cfg.Token
	if cfg.TokenEnv != "" {
		if v := os.Getenv(cfg.TokenEnv); v != "" {
			token = v
		}
	}
	if token == "" {
		return nil, fmt.Errorf("%w: resolved token is empty", auth.ErrProviderNotConfigured)
	}
	id := cfg.Identity
	if id.Source == "" {
		id.Source = ProviderName
	}
	return &Provider{
		expected: []byte(token),
		identity: id,
	}, nil
}

// Name implements auth.Provider.
func (p *Provider) Name() string { return ProviderName }

// Verify implements auth.Provider. Constant-time compare against the
// configured token. Mismatch yields to the next provider via ErrTokenNotForMe.
//
// Length-leak guard (review #11a): subtle.ConstantTimeCompare returns 0
// immediately when the two slices have different lengths, without
// inspecting any bytes — that early return leaks the expected token's
// length via measurable timing differences over many trials. We hash
// both sides to SHA-256 first so the comparison is always over
// equal-length (32-byte) digests. Hash → constant-time compare is the
// standard mitigation for this class.
func (p *Provider) Verify(_ context.Context, token string, _ auth.Headers) (*auth.Identity, error) {
	presented := sha256.Sum256([]byte(token))
	expected := sha256.Sum256(p.expected)
	if subtle.ConstantTimeCompare(presented[:], expected[:]) != 1 {
		return nil, auth.ErrTokenNotForMe
	}
	// Return a defensive copy so callers can't mutate the configured identity.
	id := p.identity
	if id.Groups != nil {
		id.Groups = slices.Clone(id.Groups)
	}
	if id.Claims != nil {
		id.Claims = maps.Clone(id.Claims)
	}
	return &id, nil
}

func init() {
	auth.Register(ProviderName, func(settings map[string]any) (auth.Provider, error) {
		var cfg Config
		if err := auth.UnmarshalSettings(settings, &cfg); err != nil {
			return nil, err
		}
		// "internal" is the runtime loopback's descriptive Source. The
		// on-behalf-of graft trusts an unexported marker (not this string),
		// so claiming it grants nothing — but reject it anyway so a user
		// provider can't masquerade as the loopback in audit trails
		// (review #356). The runtime's own loopback bypasses this factory
		// (direct New()).
		if cfg.Identity.Source == "internal" {
			return nil, fmt.Errorf("identity.source %q is reserved for the runtime loopback", "internal")
		}
		return New(cfg)
	})
}
