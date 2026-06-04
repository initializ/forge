package runtime

import (
	"fmt"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/types"
)

// PopulateSecuritySchemes derives the A2A SecuritySchemes + Security
// requirements from the agent's configured auth chain (forge.yaml
// auth: block) and writes them into the card. The function is
// additive: it preserves any schemes the caller has already set.
//
// The mapping mirrors what Forge's auth middleware actually accepts:
//
//	static_token   → http + bearer (opaque token)
//	http_verifier  → http + bearer (token validated by external endpoint)
//	oidc           → openIdConnect with issuer discovery URL
//	azure_ad       → openIdConnect (AAD exposes a standard OIDC discovery)
//	gcp_iap        → apiKey in header (X-Goog-Iap-Jwt-Assertion)
//	aws_sigv4      → http + bearer with bearerFormat "forge-aws-v1"
//	                 (forge-specific Sigv4-reflection-via-bearer pattern)
//
// Every chain entry produces one scheme name = entry's Name field (or
// Type when Name is empty). The Security array carries one map per
// scheme; the outer list is OR (any one suffices), matching Forge's
// first-match-wins chain semantics.
//
// When no auth is configured, no schemes are emitted — A2A 0.3.0
// treats absence as "no auth required."
func PopulateSecuritySchemes(card *a2a.AgentCard, cfg *types.ForgeConfig) {
	if cfg == nil || len(cfg.Auth.Providers) == 0 {
		return
	}
	if card.SecuritySchemes == nil {
		card.SecuritySchemes = map[string]*a2a.SecurityScheme{}
	}

	for _, p := range cfg.Auth.Providers {
		name := p.Name
		if name == "" {
			name = p.Type
		}
		if _, exists := card.SecuritySchemes[name]; exists {
			// Caller already configured this scheme — don't clobber.
			continue
		}
		scheme := schemeFromProvider(p)
		if scheme == nil {
			continue
		}
		card.SecuritySchemes[name] = scheme

		// Each provider becomes its own OR-entry in the Security array
		// — Forge's chain is first-match-wins, so presenting any one of
		// the configured credentials satisfies the requirement.
		card.Security = append(card.Security, map[string][]string{
			name: {},
		})
	}
}

// schemeFromProvider maps one AuthProvider entry to its A2A
// SecurityScheme. Returns nil for provider types we don't have a
// well-defined mapping for; callers should treat those as
// Forge-internal (the auth chain still enforces them, the card just
// doesn't advertise the credential shape).
func schemeFromProvider(p types.AuthProvider) *a2a.SecurityScheme {
	switch p.Type {
	case "static_token":
		return &a2a.SecurityScheme{
			Type:        "http",
			Scheme:      "bearer",
			Description: "Static shared-secret token in the Authorization header.",
		}
	case "http_verifier":
		return &a2a.SecurityScheme{
			Type:        "http",
			Scheme:      "bearer",
			Description: "Opaque bearer token verified by an external HTTP endpoint.",
		}
	case "oidc":
		issuer, _ := p.Settings["issuer"].(string)
		s := &a2a.SecurityScheme{
			Type:        "openIdConnect",
			Description: "JWT issued by an OIDC provider.",
		}
		if issuer != "" {
			s.OpenIDConnectURL = trimTrailingSlash(issuer) + "/.well-known/openid-configuration"
		}
		return s
	case "azure_ad":
		tenant, _ := p.Settings["tenant_id"].(string)
		s := &a2a.SecurityScheme{
			Type:        "openIdConnect",
			Description: "Microsoft Entra ID (Azure AD) token.",
		}
		if tenant != "" {
			s.OpenIDConnectURL = fmt.Sprintf(
				"https://login.microsoftonline.com/%s/v2.0/.well-known/openid-configuration",
				tenant,
			)
		}
		return s
	case "gcp_iap":
		return &a2a.SecurityScheme{
			Type:        "apiKey",
			In:          "header",
			Name:        "X-Goog-Iap-Jwt-Assertion",
			Description: "GCP Identity-Aware Proxy assertion forwarded by the load balancer.",
		}
	case "aws_sigv4":
		return &a2a.SecurityScheme{
			Type:         "http",
			Scheme:       "bearer",
			BearerFormat: "forge-aws-v1",
			Description:  "AWS Sigv4 pre-signed STS GetCallerIdentity URL wrapped as a Bearer token.",
		}
	}
	return nil
}

// trimTrailingSlash strips a single trailing "/" from s if present.
// Used so the discovery URL is well-formed regardless of how operators
// wrote the issuer in forge.yaml.
func trimTrailingSlash(s string) string {
	if len(s) > 0 && s[len(s)-1] == '/' {
		return s[:len(s)-1]
	}
	return s
}
