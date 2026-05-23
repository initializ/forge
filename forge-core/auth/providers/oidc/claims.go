package oidc

import (
	"maps"

	"github.com/golang-jwt/jwt/v5"

	"github.com/initializ/forge/forge-core/auth"
)

// ClaimMap configures which JWT claim names are read into each Identity
// field. Empty fields fall back to OIDC standard claim names.
type ClaimMap struct {
	UserID      string `yaml:"user_id,omitempty"`
	Email       string `yaml:"email,omitempty"`
	OrgID       string `yaml:"org_id,omitempty"`
	WorkspaceID string `yaml:"workspace_id,omitempty"`
	Groups      string `yaml:"groups,omitempty"`
}

// defaults fills empty ClaimMap fields with OIDC-standard claim names.
// Returns a copy — the input is not mutated.
func (m ClaimMap) defaults() ClaimMap {
	if m.UserID == "" {
		m.UserID = "sub"
	}
	if m.Email == "" {
		m.Email = "email"
	}
	if m.OrgID == "" {
		m.OrgID = "org_id"
	}
	if m.WorkspaceID == "" {
		m.WorkspaceID = "workspace_id"
	}
	if m.Groups == "" {
		m.Groups = "groups"
	}
	return m
}

// mapClaims builds an Identity from JWT claims using the configured
// ClaimMap. Header overrides (X-Org-ID and variants) take precedence over
// claim-derived OrgID — this preserves the per-request tenant routing
// behavior the http_verifier provider had.
//
// The raw claim map is also copied into Identity.Claims so that downstream
// authorization layers (Phase 4+) can read provider-specific claims
// without going back through the JWT.
func mapClaims(m ClaimMap, claims jwt.MapClaims, headers auth.Headers) *auth.Identity {
	cm := m.defaults()
	id := &auth.Identity{
		UserID:      stringClaim(claims, cm.UserID),
		Email:       stringClaim(claims, cm.Email),
		OrgID:       stringClaim(claims, cm.OrgID),
		WorkspaceID: stringClaim(claims, cm.WorkspaceID),
		Groups:      stringSliceClaim(claims, cm.Groups),
		Claims:      copyClaims(claims),
		Source:      ProviderName,
	}

	// Header override: X-Org-ID (and lowercase variants) takes precedence
	// over the claim-derived OrgID. The http_verifier provider has the
	// same behavior for parity.
	for _, key := range []string{"X-Org-ID", "org-id", "org_id"} {
		if v := headers.Get(key); v != "" {
			id.OrgID = v
			break
		}
	}

	return id
}

// stringClaim returns the named claim as a string, or "" if missing or
// not a string.
func stringClaim(claims jwt.MapClaims, name string) string {
	v, ok := claims[name]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// stringSliceClaim returns the named claim as a string slice. Tolerates
// the common shapes IdPs use: []string, []any of strings, or a single
// string (which becomes a one-element slice).
func stringSliceClaim(claims jwt.MapClaims, name string) []string {
	v, ok := claims[name]
	if !ok {
		return nil
	}
	switch val := v.(type) {
	case []string:
		out := make([]string, len(val))
		copy(out, val)
		return out
	case []any:
		out := make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		if val == "" {
			return nil
		}
		return []string{val}
	default:
		return nil
	}
}

// copyClaims returns a shallow copy of the claim map so the caller can
// expose it on Identity without sharing the underlying map with the
// parsed-token state.
//
// WARNING (review #11f): the returned map contains EVERY claim the IdP
// emitted on the token — `sub`, `email`, `iss`, `aud`, `exp`, `iat`,
// `nbf`, plus any custom claims the issuer adds (group memberships,
// internal IDs, profile fields, sometimes raw PII). Downstream consumers
// reading Identity.Claims will see all of it.
//
// Recommendations for consumers:
//   - Prefer the mapped, typed fields on Identity (UserID, Email, OrgID,
//     Groups) where they suffice — those have a fixed contract.
//   - Treat Identity.Claims as an escape hatch for provider-specific
//     authorization logic, not as something to log or relay verbatim.
//   - When a future authz layer needs a claim-allowlist, that's the
//     right place to add it — not here. This package serves the raw
//     auth principal; filtering is policy.
func copyClaims(claims jwt.MapClaims) map[string]any {
	if len(claims) == 0 {
		return nil
	}
	out := make(map[string]any, len(claims))
	maps.Copy(out, claims)
	return out
}
