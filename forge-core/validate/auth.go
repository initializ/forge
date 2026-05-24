package validate

import (
	"fmt"

	"github.com/initializ/forge/forge-core/types"
)

// knownAuthProviderTypes is the closed set of auth provider types that
// validate accepts. It is intentionally NOT derived from auth.RegisteredTypes()
// — validation must work at build time (forge validate) without importing
// the runtime provider implementations, which is what allows the forge-core
// `validate` package to stay free of side-effect imports.
//
// New provider types must be added here when they ship.
var knownAuthProviderTypes = map[string]bool{
	"http_verifier": true,
	"static_token":  true,
	"oidc":          true,
	// Phase 2 (v0.11.0):
	"aws_sigv4": true,
	"gcp_iap":   true,
	"azure_ad":  true,
}

// KnownAuthProviderSettings is the closed set of YAML keys each provider
// type accepts. Mirrors the `yaml:` tags on each provider's Config struct;
// must be kept in sync when new fields are added. Internal-only struct
// fields (those carrying `yaml:"-"`) are intentionally absent — they
// can only be set by another Go package, not via forge.yaml or the
// Web UI's create-payload.
//
// Two callers consume this map:
//
//  1. ValidateAuthConfig emits a *warning* per unknown key during
//     `forge validate`, so a typo like `aud:` instead of `audience:`
//     gets surfaced loudly.
//
//  2. The Web UI handler (forge-ui handlers_create.go) filters its
//     incoming Settings map through FilterKnownSettings before
//     forwarding to scaffold — closing the exploit chain where a
//     malicious POST `{"settings": {"audience": "x", "evil": "y"}}`
//     would otherwise drop `evil:` into forge.yaml verbatim.
var KnownAuthProviderSettings = map[string]map[string]bool{
	"http_verifier": {
		"url":         true,
		"default_org": true,
		"timeout":     true,
	},
	"static_token": {
		"token":     true,
		"token_env": true,
	},
	"oidc": {
		"issuer":         true,
		"audience":       true,
		"client_id":      true,
		"jwks_url":       true,
		"jwks_cache_ttl": true,
		"clock_skew":     true,
		"claim_map":      true,
	},
	"aws_sigv4": {
		"region":             true,
		"audience":           true,
		"allowed_principals": true,
		"allowed_accounts":   true,
		"identity_cache_ttl": true,
		"sts_endpoint":       true, // documented test override, intentionally YAML-reachable
		"http_timeout":       true,
		"max_token_expires":  true,
		"clock_skew":         true,
	},
	"gcp_iap": {
		"audience":         true,
		"jwks_refresh_ttl": true,
		"http_timeout":     true,
	},
	"azure_ad": {
		"tenant_id":          true,
		"audience":           true,
		"allow_multi_tenant": true,
		"allowed_tenants":    true,
		"groups_mode":        true,
		"graph_timeout":      true,
		"jwks_cache_ttl":     true,
		// graph_endpoint intentionally omitted — yaml:"-" on the Config field
	},
}

// FilterKnownSettings returns a copy of settings with any keys not in
// the whitelist for providerType dropped. Use this at the boundary
// between untrusted input (Web UI POST) and persistence (forge.yaml
// scaffold) so unknown keys never reach disk.
//
// For unknown providerType (returns nil from the whitelist lookup), the
// input is passed through unchanged — let the ValidateAuthConfig
// "unknown type" error catch that case instead.
func FilterKnownSettings(providerType string, settings map[string]any) map[string]any {
	known, ok := KnownAuthProviderSettings[providerType]
	if !ok {
		return settings
	}
	out := make(map[string]any, len(settings))
	for k, v := range settings {
		if known[k] {
			out[k] = v
		}
	}
	return out
}

// ValidateAuthConfig adds errors and warnings for a forge.yaml auth: block.
// Empty AuthConfig is valid (legacy --auth-url path remains the fallback).
func ValidateAuthConfig(cfg types.AuthConfig, r *ValidationResult) {
	if len(cfg.Providers) == 0 {
		if cfg.Required {
			r.Errors = append(r.Errors, "auth.required is true but auth.providers is empty")
		}
		return
	}

	seenNames := map[string]int{}
	for i, p := range cfg.Providers {
		prefix := fmt.Sprintf("auth.providers[%d]", i)

		if p.Type == "" {
			r.Errors = append(r.Errors, prefix+": type is required")
			continue
		}
		if !knownAuthProviderTypes[p.Type] {
			r.Errors = append(r.Errors, fmt.Sprintf("%s: unknown type %q (known: http_verifier, static_token, oidc, aws_sigv4, gcp_iap, azure_ad)", prefix, p.Type))
			continue
		}

		if p.Name != "" {
			seenNames[p.Name]++
			if seenNames[p.Name] > 1 {
				r.Warnings = append(r.Warnings, fmt.Sprintf("%s: duplicate name %q across providers", prefix, p.Name))
			}
		}

		validateProviderSettings(prefix, p, r)
	}
}

// validateProviderSettings checks the type-specific required keys in the
// settings block. We re-validate here (the provider's own Validate()
// runs at runtime construction) so `forge validate` catches errors before
// `forge run`.
func validateProviderSettings(prefix string, p types.AuthProvider, r *ValidationResult) {
	// Warn on any keys the provider doesn't recognize. Loose vs. error
	// because some operators stash custom annotations (legacy practice
	// from pre-Phase-2 configs); the Web UI handler additionally filters
	// at write-time so the actual scaffold-poisoning chain is closed
	// there (see forge-ui/handlers_create.go).
	if known, ok := KnownAuthProviderSettings[p.Type]; ok {
		for k := range p.Settings {
			if !known[k] {
				r.Warnings = append(r.Warnings,
					fmt.Sprintf("%s (%s): unknown settings key %q — typo, or a key from a future provider version?", prefix, p.Type, k))
			}
		}
	}

	switch p.Type {
	case "http_verifier":
		if asString(p.Settings, "url") == "" {
			r.Errors = append(r.Errors, prefix+" (http_verifier): settings.url is required")
		}
	case "static_token":
		if asString(p.Settings, "token") == "" && asString(p.Settings, "token_env") == "" {
			r.Errors = append(r.Errors, prefix+" (static_token): settings.token or settings.token_env is required")
		}
		if asString(p.Settings, "token") != "" {
			r.Warnings = append(r.Warnings, prefix+" (static_token): literal 'token' in YAML is a footgun — prefer 'token_env'")
		}
	case "oidc":
		if asString(p.Settings, "issuer") == "" {
			r.Errors = append(r.Errors, prefix+" (oidc): settings.issuer is required")
		}
		if asString(p.Settings, "audience") == "" {
			r.Errors = append(r.Errors, prefix+" (oidc): settings.audience is required")
		}
	case "aws_sigv4":
		if asString(p.Settings, "region") == "" {
			r.Errors = append(r.Errors, prefix+" (aws_sigv4): settings.region is required")
		}
		// allowed_accounts entries must be 12-digit AWS account IDs.
		// Catches typos at validate-time so a misconfig doesn't silently
		// become an unreachable pattern.
		if accts, ok := p.Settings["allowed_accounts"].([]any); ok {
			for i, raw := range accts {
				s, _ := raw.(string)
				if len(s) != 12 || !isAllDigits(s) {
					r.Errors = append(r.Errors,
						fmt.Sprintf("%s (aws_sigv4): allowed_accounts[%d]=%q must be a 12-digit AWS account ID", prefix, i, s))
				}
			}
		}
	case "gcp_iap":
		if asString(p.Settings, "audience") == "" {
			r.Errors = append(r.Errors, prefix+" (gcp_iap): settings.audience is required (GCP backend service ID)")
		}
	case "azure_ad":
		if asString(p.Settings, "audience") == "" {
			r.Errors = append(r.Errors, prefix+" (azure_ad): settings.audience is required")
		}
		// tenant_id may be omitted ONLY when allow_multi_tenant is true.
		multi, _ := p.Settings["allow_multi_tenant"].(bool)
		if !multi && asString(p.Settings, "tenant_id") == "" {
			r.Errors = append(r.Errors, prefix+" (azure_ad): settings.tenant_id is required unless allow_multi_tenant=true")
		}
		// allowed_tenants only makes sense with multi-tenant.
		hasAllowed := false
		switch v := p.Settings["allowed_tenants"].(type) {
		case []any:
			hasAllowed = len(v) > 0
		case []string:
			hasAllowed = len(v) > 0
		}
		if !multi && hasAllowed {
			r.Errors = append(r.Errors,
				prefix+" (azure_ad): allowed_tenants is only meaningful when allow_multi_tenant=true")
		}
		// "Any-tenant mode" warning: multi-tenant + empty allowed_tenants
		// admits any Entra tenant globally. Documented trade-off, but
		// warn so operators don't ship it by accident.
		if multi && !hasAllowed {
			r.Warnings = append(r.Warnings,
				prefix+" (azure_ad): allow_multi_tenant=true with no allowed_tenants list "+
					"admits any Entra tenant globally — set allowed_tenants if you want to "+
					"restrict to specific partner tenants")
		}
		if mode := asString(p.Settings, "groups_mode"); mode != "" && mode != "claim" && mode != "graph" {
			r.Errors = append(r.Errors, fmt.Sprintf("%s (azure_ad): groups_mode must be 'claim' or 'graph', got %q", prefix, mode))
		}
	}
}

// asString reads a string-valued setting, returning "" for missing or
// non-string values.
func asString(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// isAllDigits reports whether s consists only of ASCII digits.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
