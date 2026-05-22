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
	// "okta": true,  // Phase 3 (v0.11.0)
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
			r.Errors = append(r.Errors, fmt.Sprintf("%s: unknown type %q (known: http_verifier, static_token, oidc)", prefix, p.Type))
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
