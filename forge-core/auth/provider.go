// Package auth provides bearer-token authentication for the A2A server,
// built around a pluggable Provider chain.
//
// Each Provider claims tokens it recognizes and rejects the rest with
// ErrTokenNotForMe, allowing a ChainProvider to compose multiple
// providers in a first-match-wins fashion. The error return is the only
// signal for the verification outcome — there is intentionally no
// Identity.Valid field, because a nil-error return is the contract for
// "this token is valid."
//
// New providers live under forge-core/auth/providers/<name>/ and register
// themselves via init() against the package-level registry, mirroring the
// database/sql driver pattern.
package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// Provider verifies a bearer token and returns the caller's Identity.
//
// Implementations must:
//   - Return (id, nil) on a verified token.
//   - Return (nil, ErrTokenNotForMe) when the token is not for this provider
//     (so the ChainProvider can try the next provider).
//   - Return (nil, ErrTokenRejected) when the token is recognized but denied
//     (e.g., revoked, expired, untrusted issuer).
//   - Return (nil, ErrInvalidToken) when the token is malformed or
//     cryptographically invalid.
//   - Return (nil, other-error) for transient failures (network, etc.).
//     The ChainProvider treats these as fatal (fail-closed) — it does NOT
//     fall through to the next provider on infrastructure errors, because
//     doing so would allow attackers to evade a temporarily-down provider.
type Provider interface {
	Name() string
	Verify(ctx context.Context, token string, headers Headers) (*Identity, error)
}

// Identity is the authenticated principal extracted by a Provider.
//
// There is intentionally no Valid field — a non-nil *Identity returned
// alongside a nil error is the only "valid" signal. See package comment.
type Identity struct {
	UserID      string         `json:"user_id,omitempty"`
	Email       string         `json:"email,omitempty"`
	OrgID       string         `json:"org_id,omitempty"`
	WorkspaceID string         `json:"workspace_id,omitempty"`
	Groups      []string       `json:"groups,omitempty"`
	Claims      map[string]any `json:"claims,omitempty"`
	// Source records which provider verified the identity (e.g., "oidc",
	// "http_verifier", "static_token"). Useful for audit logs and debugging.
	Source string `json:"source,omitempty"`
}

// Headers is a case-insensitive view over selected request headers passed
// to providers. Providers should not assume any particular casing.
type Headers map[string]string

// Get returns the value for the given header, matched case-insensitively.
func (h Headers) Get(key string) string {
	if v, ok := h[key]; ok {
		return v
	}
	lower := strings.ToLower(key)
	for k, v := range h {
		if strings.ToLower(k) == lower {
			return v
		}
	}
	return ""
}

// HeadersFromRequest extracts the well-known headers providers may use.
// Keep this list narrow — providers should be explicit about the contract.
func HeadersFromRequest(r *http.Request) Headers {
	return Headers{
		"X-Org-ID":     r.Header.Get("X-Org-ID"),
		"X-Request-ID": r.Header.Get("X-Request-ID"),
		"org-id":       r.Header.Get("org-id"),
		"org_id":       r.Header.Get("org_id"),
	}
}

// TokenKind classifies a presented bearer token structurally — useful for
// audit logging without leaking the token itself.
//
// "jwt"     → three base64url segments separated by dots
// "opaque"  → anything else (Okta access tokens, custom verifier tokens, dev secrets)
//
// This is a CHEAP structural check — it does not parse or validate.
// Never log the token; this helper is safe to log.
func TokenKind(token string) string {
	if token == "" {
		return "empty"
	}
	dots := 0
	for i := 0; i < len(token); i++ {
		if token[i] == '.' {
			dots++
			if dots > 2 {
				return "opaque"
			}
		}
	}
	if dots == 2 {
		return "jwt"
	}
	return "opaque"
}

// Sentinel errors that Providers and the ChainProvider use to signal outcomes.
//
//   - ErrTokenNotForMe         → provider does not recognize this token shape;
//     ChainProvider should try the next provider.
//   - ErrTokenRejected         → provider recognized the token and denied it;
//     ChainProvider stops and the middleware writes 401.
//   - ErrInvalidToken          → token is malformed or cryptographically invalid;
//     ChainProvider stops and the middleware writes 401.
//   - ErrProviderUnavailable   → the verifier / IdP is unreachable or returned
//     a transport-layer error (5xx, network timeout, garbage response). The
//     token MAY be valid — we just can't say. ChainProvider stops (fail-closed,
//     same as ErrInvalidToken), but the audit signal is distinct so operators
//     don't chase a token issue when the actual problem is provider downtime.
//   - ErrProviderNotConfigured → returned by New(); never by Verify().
var (
	ErrTokenNotForMe         = errors.New("auth: token not for this provider")
	ErrTokenRejected         = errors.New("auth: token rejected")
	ErrInvalidToken          = errors.New("auth: invalid token")
	ErrProviderUnavailable   = errors.New("auth: provider unavailable")
	ErrProviderNotConfigured = errors.New("auth: provider not configured")
)
