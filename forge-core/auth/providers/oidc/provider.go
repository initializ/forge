// Package oidc implements an auth.Provider that verifies JWT bearer tokens
// against an OpenID Connect issuer.
//
// It works with any compliant OIDC provider — Auth0, Keycloak, Azure AD,
// Google Workspace, Ping, JumpCloud, and (in OIDC-only mode) Okta.
//
// Behavior at a glance:
//
//   - On first verify, the discovery document is fetched once from
//     {issuer}/.well-known/openid-configuration and cached forever (the
//     issuer is stable per OIDC spec).
//
//   - JWKS is fetched lazily and cached. On unknown-kid, the JWKS is
//     refetched once before declaring the key unknown.
//
//   - The signing algorithm is taken from the JWKS entry, NOT from the
//     token header. This defends against algorithm-confusion attacks
//     (e.g., a token claiming `alg: HS256` against an RSA JWKS).
//
//   - alg=none and HMAC algorithms (HS256/384/512) are never accepted.
//
//   - Standard claims are validated: iss exact match, aud contains
//     configured Audience (with optional azp == ClientID fallback),
//     exp/nbf within ClockSkew leeway.
//
//   - Claim → Identity mapping is configurable via ClaimMap. The X-Org-ID
//     header overrides the claim-derived OrgID for per-request tenant
//     routing.
package oidc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/initializ/forge/forge-core/auth"
)

// ProviderName is the type name used to register and reference this
// provider in the auth registry.
const ProviderName = "oidc"

// Default operational tunables.
const (
	DefaultClockSkew   = 30 * time.Second
	DefaultHTTPTimeout = 10 * time.Second
)

// Config controls the OIDC provider.
type Config struct {
	// Issuer is the full OIDC issuer URL (no trailing slash). Required.
	// The token's `iss` claim must match this value exactly.
	Issuer string `yaml:"issuer"`

	// Audience is the expected `aud` claim value. Required. If the token
	// has multiple audiences, the configured Audience must be one of them.
	Audience string `yaml:"audience"`

	// ClientID is an optional secondary audience check: if set, a token
	// whose `aud` does not contain Audience is still accepted when its
	// `azp` (authorized party) claim equals ClientID.
	ClientID string `yaml:"client_id,omitempty"`

	// JWKSURL overrides the JWKS endpoint discovered via the OIDC
	// discovery document. Most users leave this empty.
	JWKSURL string `yaml:"jwks_url,omitempty"`

	// JWKSCacheTTL caps the maximum age of cached JWKS keys. Defaults to
	// 1 hour. Values below 5 minutes are silently clamped up to avoid
	// hammering the IdP.
	JWKSCacheTTL time.Duration `yaml:"jwks_cache_ttl,omitempty"`

	// ClockSkew is the leeway applied to `exp` and `nbf` validation.
	// Defaults to 30 seconds.
	ClockSkew time.Duration `yaml:"clock_skew,omitempty"`

	// ClaimMap configures which JWT claim names map to Identity fields.
	// Empty fields use OIDC defaults (sub, email, org_id, …).
	ClaimMap ClaimMap `yaml:"claim_map,omitempty"`

	// HTTPClient overrides the default client. Injectable for tests.
	HTTPClient *http.Client `yaml:"-"`
}

// Validate returns ErrProviderNotConfigured when required fields are
// missing.
func (c Config) Validate() error {
	if c.Issuer == "" {
		return fmt.Errorf("%w: issuer required", auth.ErrProviderNotConfigured)
	}
	if c.Audience == "" {
		return fmt.Errorf("%w: audience required", auth.ErrProviderNotConfigured)
	}
	return nil
}

// normalize returns a copy of the Config with the Issuer in canonical
// form (trailing slash stripped). Called once at New() so every
// downstream comparison — discovery doc check, JWT iss validation,
// JWKS URL construction — uses the same form.
//
// Why this matters: OIDC IdPs are inconsistent about trailing slashes.
// Auth0 emits "iss": "https://tenant.auth0.com/", many enterprise
// Okta tenants emit without, etc. Operators paste whatever they see in
// their admin UI into forge.yaml. We trim once at the boundary so the
// mismatch never reaches a comparison.
func (c Config) normalize() Config {
	c.Issuer = strings.TrimRight(c.Issuer, "/")
	return c
}

// Provider implements auth.Provider for OIDC issuers.
type Provider struct {
	cfg       Config
	client    *http.Client
	discovery *discovery
	jwks      *jwksCache
	parser    *jwt.Parser
	clockSkew time.Duration
}

// New constructs a Provider after validating cfg. No network I/O happens
// here — discovery and JWKS are fetched lazily on first Verify.
func New(cfg Config) (*Provider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	// Normalize trailing slash once so every downstream comparison
	// (discovery doc, token iss, JWKS URL construction) uses the same
	// canonical form. Review finding #2.
	cfg = cfg.normalize()
	if cfg.ClockSkew == 0 {
		cfg.ClockSkew = DefaultClockSkew
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: DefaultHTTPTimeout}
	}

	p := &Provider{
		cfg:       cfg,
		client:    client,
		clockSkew: cfg.ClockSkew,
	}

	p.discovery = newDiscovery(cfg.Issuer, client)
	p.jwks = newJWKSCache(p.resolveJWKSURL, client, cfg.JWKSCacheTTL)

	// Configure the JWT parser once.
	//
	// Issuer is validated manually (post-parse) instead of via
	// jwt.WithIssuer — that helper does strict string equality on the
	// token's iss claim, which fails when the IdP emits with a trailing
	// slash but our config normalized it off (or vice versa). The manual
	// check trims both sides. Same reasoning as the discovery doc
	// comparison in EnsureLoaded.
	//
	// Audience is similarly manual because of the azp fallback below.
	parserOpts := []jwt.ParserOption{
		jwt.WithValidMethods(allowedAlgNames()),
		jwt.WithLeeway(cfg.ClockSkew),
		jwt.WithExpirationRequired(),
	}
	p.parser = jwt.NewParser(parserOpts...)

	return p, nil
}

// Name implements auth.Provider.
func (p *Provider) Name() string { return ProviderName }

// resolveJWKSURL returns the configured override JWKSURL if set, otherwise
// loads discovery and returns the discovered jwks_uri.
func (p *Provider) resolveJWKSURL(ctx context.Context) (string, error) {
	if p.cfg.JWKSURL != "" {
		return p.cfg.JWKSURL, nil
	}
	if err := p.discovery.EnsureLoaded(ctx); err != nil {
		return "", err
	}
	return p.discovery.JWKSURI(), nil
}

// Verify implements auth.Provider.
//
// The verification flow:
//  1. Parse token structurally (without signature verification yet).
//  2. Extract kid from header; reject tokens without kid.
//  3. Look up kid in JWKS cache (refreshing on miss).
//  4. Cross-check token's `alg` against JWKS-declared alg (defends against
//     algorithm confusion).
//  5. Re-parse the token with the resolved key, validating iss/exp/nbf.
//  6. Validate audience (with azp fallback if ClientID is configured).
//  7. Map claims to Identity, applying header overrides.
func (p *Provider) Verify(ctx context.Context, tokenStr string, headers auth.Headers) (*auth.Identity, error) {
	// Quick structural check before doing crypto work. JWTs are three
	// base64url segments separated by dots.
	if !looksLikeJWT(tokenStr) {
		return nil, auth.ErrTokenNotForMe
	}

	// First pass: parse the header to extract kid + alg without verifying
	// the signature.
	parsedHeader, _, err := jwt.NewParser().ParseUnverified(tokenStr, jwt.MapClaims{})
	if err != nil {
		return nil, fmt.Errorf("%w: parse header: %w", auth.ErrInvalidToken, err)
	}
	kid, _ := parsedHeader.Header["kid"].(string)
	if kid == "" {
		return nil, fmt.Errorf("%w: token header missing kid", auth.ErrInvalidToken)
	}
	headerAlg, _ := parsedHeader.Header["alg"].(string)
	if !isAllowedAlg(headerAlg) {
		// Catches alg=none, HMAC, etc. before any further work.
		return nil, fmt.Errorf("%w: disallowed alg %q", auth.ErrInvalidToken, headerAlg)
	}

	// Look up the key.
	key, err := p.jwks.Get(ctx, kid)
	if err != nil {
		if errors.Is(err, ErrKeyNotFound) {
			return nil, fmt.Errorf("%w: kid %q not found in JWKS", auth.ErrInvalidToken, kid)
		}
		return nil, err // transport / decode error — fail-closed via chain
	}

	// Cross-check: the JWKS-declared alg must match the token's alg.
	// This is the algorithm-confusion defense.
	if key.Alg != headerAlg {
		return nil, fmt.Errorf("%w: token alg %q does not match JWKS alg %q for kid %q",
			auth.ErrInvalidToken, headerAlg, key.Alg, kid)
	}

	// Second pass: verify signature + standard claims (iss/exp/nbf).
	claims := jwt.MapClaims{}
	_, err = p.parser.ParseWithClaims(tokenStr, claims, func(_ *jwt.Token) (any, error) {
		return key.Public, nil
	})
	if err != nil {
		return nil, classifyJWTError(err)
	}

	// Issuer validation (manual — trims trailing slash on both sides so
	// IdPs that emit iss with/without a trailing slash interop with
	// configs that use the opposite form). See Config.normalize and
	// review finding #2.
	if err := p.checkIssuer(claims); err != nil {
		return nil, err
	}

	// Audience validation (with azp fallback).
	if err := p.checkAudience(claims); err != nil {
		return nil, err
	}

	return mapClaims(p.cfg.ClaimMap, claims, headers), nil
}

// checkIssuer validates the token's `iss` claim against the configured
// Issuer. Both values are trimmed of trailing slashes before comparison
// so trailing-slash drift between the IdP and the operator's configured
// value doesn't cause spurious rejections.
//
// Security note: trimming a single trailing slash cannot enable issuer
// impersonation — "https://attacker.example" and "https://legit.example"
// remain distinct. The trim is purely a normalization fix.
func (p *Provider) checkIssuer(claims jwt.MapClaims) error {
	iss, _ := claims["iss"].(string)
	if iss == "" {
		return fmt.Errorf("%w: missing iss claim", auth.ErrTokenRejected)
	}
	if strings.TrimRight(iss, "/") != p.cfg.Issuer {
		return fmt.Errorf("%w: issuer %q does not match configured %q",
			auth.ErrTokenRejected, iss, p.cfg.Issuer)
	}
	return nil
}

// looksLikeJWT does a cheap structural check: exactly three non-empty
// dot-separated segments. Used to yield to the next provider on non-JWT
// tokens (e.g., opaque tokens that another chain entry would handle).
func looksLikeJWT(s string) bool {
	if len(s) < 5 {
		return false
	}
	segments := 1
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			segments++
			if segments > 3 {
				return false
			}
		}
	}
	return segments == 3
}

// checkAudience validates that the token's `aud` claim contains the
// configured Audience. If ClientID is set and aud does not contain
// Audience, accepts when `azp` claim equals ClientID.
func (p *Provider) checkAudience(claims jwt.MapClaims) error {
	auds := audienceSlice(claims)
	if slices.Contains(auds, p.cfg.Audience) {
		return nil
	}
	if p.cfg.ClientID != "" {
		if azp, _ := claims["azp"].(string); azp == p.cfg.ClientID {
			return nil
		}
	}
	return fmt.Errorf("%w: audience %v does not match configured %q", auth.ErrTokenRejected, auds, p.cfg.Audience)
}

// audienceSlice normalizes the `aud` claim into []string. RFC 7519 allows
// `aud` to be either a string or a string array.
func audienceSlice(claims jwt.MapClaims) []string {
	switch v := claims["aud"].(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return v
	default:
		return nil
	}
}

// classifyJWTError maps a parsing/verification error from jwt/v5 to one of
// the auth-package sentinels.
func classifyJWTError(err error) error {
	switch {
	case errors.Is(err, jwt.ErrTokenExpired):
		return fmt.Errorf("%w: token expired", auth.ErrTokenRejected)
	case errors.Is(err, jwt.ErrTokenNotValidYet):
		return fmt.Errorf("%w: token not yet valid", auth.ErrTokenRejected)
	case errors.Is(err, jwt.ErrTokenInvalidIssuer):
		return fmt.Errorf("%w: issuer mismatch", auth.ErrTokenRejected)
	case errors.Is(err, jwt.ErrTokenSignatureInvalid):
		return fmt.Errorf("%w: signature invalid", auth.ErrInvalidToken)
	case errors.Is(err, jwt.ErrTokenMalformed):
		return fmt.Errorf("%w: token malformed", auth.ErrInvalidToken)
	case errors.Is(err, jwt.ErrTokenUnverifiable):
		return fmt.Errorf("%w: token unverifiable", auth.ErrInvalidToken)
	case errors.Is(err, jwt.ErrTokenRequiredClaimMissing):
		return fmt.Errorf("%w: required claim missing", auth.ErrInvalidToken)
	default:
		return fmt.Errorf("%w: %w", auth.ErrInvalidToken, err)
	}
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
