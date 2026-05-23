// Package gcp_iap authenticates requests that come through GCP's
// Identity-Aware Proxy. IAP terminates user authentication at the
// HTTPS load balancer and forwards a signed JWT in the
// X-Goog-Iap-Jwt-Assertion header on every authenticated request.
// Forge verifies that JWT against IAP's well-known JWKS and stamps
// an Identity from the claims.
//
// Decision §9.4: the IAP issuer ("https://cloud.google.com/iap")
// and JWKS URL ("https://www.gstatic.com/iap/verify/public_key-jwk")
// are HARDCODED. They are the only stable contract GCP exposes.
// Any override knob would be a footgun (operators could be tricked
// into trusting an attacker's JWKS).
//
// Decision §9.5: reads X-Goog-Iap-Jwt-Assertion from the widened
// Headers map (PR 1).
//
// Audit reason codes (Phase 1 contract):
//
//	rejected             — iss/aud mismatch, expired, bad signature
//	invalid              — alg != ES256, missing sub/email, malformed
//	provider_unavailable — JWKS fetch failed AND no prior key cached
//	not_for_me           — header absent → next provider
package gcp_iap

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/initializ/forge/forge-core/auth"
)

// ProviderName is the registry name.
const ProviderName = "gcp_iap"

// Hardcoded IAP endpoints (decision §9.4).
const (
	iapIssuer  = "https://cloud.google.com/iap"
	iapJWKSURL = "https://www.gstatic.com/iap/verify/public_key-jwk"
)

const (
	defaultJWKSRefreshTTL = time.Hour
	defaultHTTPTimeout    = 5 * time.Second
)

// Config controls the gcp_iap provider.
type Config struct {
	// Audience is REQUIRED. It is the GCP backend service ID,
	// shaped like "/projects/PROJECT_NUMBER/global/backendServices/BACKEND_ID".
	// Operators find this in the GCP console under
	// Security → Identity-Aware Proxy → Backend Service → "Signed Header JWT Audience".
	Audience string `yaml:"audience"`

	// JWKSRefreshTTL bounds how long a cached JWKS is reused before a
	// background refresh. Default 1h — IAP rotates keys slowly.
	JWKSRefreshTTL time.Duration `yaml:"jwks_refresh_ttl,omitempty"`

	// HTTPTimeout caps the JWKS fetch. Default 5s.
	HTTPTimeout time.Duration `yaml:"http_timeout,omitempty"`
}

// Validate returns ErrProviderNotConfigured when required fields are missing.
func (c Config) Validate() error {
	if c.Audience == "" {
		return fmt.Errorf("%w: audience required (e.g. /projects/PNUM/global/backendServices/BACKEND_ID)", auth.ErrProviderNotConfigured)
	}
	return nil
}

// Provider implements auth.Provider for GCP IAP-fronted callers.
type Provider struct {
	cfg  Config
	jwks *IAPJWKSCache
}

// New constructs a Provider after validating cfg.
func New(cfg Config) (*Provider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.JWKSRefreshTTL == 0 {
		cfg.JWKSRefreshTTL = defaultJWKSRefreshTTL
	}
	if cfg.HTTPTimeout == 0 {
		cfg.HTTPTimeout = defaultHTTPTimeout
	}
	return &Provider{
		cfg:  cfg,
		jwks: NewIAPJWKSCache(iapJWKSURL, cfg.JWKSRefreshTTL, cfg.HTTPTimeout),
	}, nil
}

// Name implements auth.Provider.
func (p *Provider) Name() string { return ProviderName }

// Verify implements auth.Provider.
//
// Token is unused — the IAP assertion lives in the
// X-Goog-Iap-Jwt-Assertion header, which the middleware delivers
// via the headers map (PR 1).
func (p *Provider) Verify(ctx context.Context, _ string, headers auth.Headers) (*auth.Identity, error) {
	raw := headers.Get("X-Goog-Iap-Jwt-Assertion")
	if raw == "" {
		return nil, auth.ErrTokenNotForMe
	}

	claims, err := p.jwks.VerifyAndParse(ctx, raw)
	if err != nil {
		return nil, err // already wrapped with the right sentinel
	}

	if claims.Issuer != iapIssuer {
		return nil, fmt.Errorf("%w: iss=%q, want %q", auth.ErrTokenRejected, claims.Issuer, iapIssuer)
	}
	if !audienceContains(claims.Audience, p.cfg.Audience) {
		return nil, fmt.Errorf("%w: aud mismatch", auth.ErrTokenRejected)
	}
	if claims.Subject == "" || claims.Email == "" {
		// IAP always sets both. Absence implies a malformed/stripped
		// token, not a policy denial — return ErrInvalidToken so the
		// audit log distinguishes the two cases.
		return nil, fmt.Errorf("%w: IAP token missing sub or email", auth.ErrInvalidToken)
	}

	return &auth.Identity{
		UserID: claims.Subject,
		Email:  claims.Email,
		Source: ProviderName,
		Claims: map[string]any{
			"sub":   claims.Subject,
			"email": claims.Email,
			"hd":    claims.HD,
		},
	}, nil
}

func audienceContains(aud []string, want string) bool {
	return slices.Contains(aud, want)
}

func init() {
	auth.Register(ProviderName, func(settings map[string]any) (auth.Provider, error) {
		var cfg Config
		if err := auth.UnmarshalSettings(settings, &cfg); err != nil {
			return nil, err
		}
		return New(cfg)
	})
}
