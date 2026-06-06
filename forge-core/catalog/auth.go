package catalog

// authModes is the canonical, ordered list of A2A authentication modes.
//
// The provider-backed modes (oidc, http_verifier, aws_sigv4, gcp_iap, azure_ad)
// correspond to auth providers registered in forge-core/auth/providers; "none"
// and "custom" are wizard-only conveniences. TestAuthModesMatchRegistry guards
// against the two lists drifting apart.
var authModes = []AuthMode{
	{
		ID:          "none",
		Label:       "None",
		Description: "Anonymous access — no auth: block in forge.yaml",
		Icon:        "🔓",
	},
	{
		ID:          "oidc",
		Label:       "OIDC (JWT)",
		Description: "Auth0, Keycloak, Azure AD, Google, Okta-OIDC, …",
		Icon:        "🔐",
		Fields: []AuthField{
			{Key: "issuer", Prompt: "Issuer URL", Placeholder: "https://login.example.com", Required: true, Validation: "https_url"},
			{Key: "audience", Prompt: "Audience", Placeholder: "api://forge", Required: true, Validation: "non_empty"},
			{Key: "groups_claim", Prompt: "Groups claim (default: groups — press Enter to accept)", Placeholder: "groups", Default: "groups"},
		},
	},
	{
		ID:          "http_verifier",
		Label:       "HTTP Verifier",
		Description: "Legacy — POST tokens to your own /verify endpoint",
		Icon:        "🔁",
		Fields: []AuthField{
			{Key: "url", Prompt: "Verifier URL", Placeholder: "https://auth.example.com/verify", Required: true, Validation: "https_url"},
			{Key: "default_org", Prompt: "Default org_id (optional — press Enter to skip)"},
		},
	},
	{
		ID:          "aws_sigv4",
		Label:       "AWS Sigv4 (IAM)",
		Description: "Verify AWS-IAM callers via STS GetCallerIdentity (Phase 2)",
		Icon:        "🅰️",
		Fields: []AuthField{
			{Key: "region", Prompt: "AWS region", Placeholder: "us-east-1", Required: true, Validation: "aws_region"},
			{Key: "audience", Prompt: "Audience (informational; press Enter to skip)", Placeholder: "api://forge"},
			{Key: "allowed_accounts", Prompt: "Allowed AWS accounts (comma-separated 12-digit IDs; Enter to skip)", Placeholder: "412664885516,109887654321", Validation: "account_list"},
		},
	},
	{
		ID:          "gcp_iap",
		Label:       "GCP Identity-Aware Proxy",
		Description: "Forge behind a GCP HTTPS LB+IAP (Phase 2)",
		Icon:        "🇬",
		Fields: []AuthField{
			{Key: "audience", Prompt: "IAP audience (backend service ID from GCP console)", Placeholder: "/projects/PNUM/global/backendServices/BACKEND_ID", Required: true, Validation: "non_empty"},
		},
	},
	{
		ID:          "azure_ad",
		Label:       "Azure AD / Entra ID",
		Description: "Single-tenant Entra tokens (Phase 2 — multi-tenant via YAML)",
		Icon:        "🇦",
		Fields: []AuthField{
			{Key: "tenant_id", Prompt: "Entra tenant ID (GUID)", Placeholder: "00000000-0000-0000-0000-000000000000", Required: true, Validation: "non_empty"},
			{Key: "audience", Prompt: "Audience (Application ID URI)", Placeholder: "api://forge", Required: true, Validation: "non_empty"},
		},
	},
	{
		ID:          "custom",
		Label:       "Custom",
		Description: "Write a commented stub — I'll edit forge.yaml myself",
		Icon:        "✏️",
	},
}

// AllAuthModes returns the catalog of authentication modes in display order.
func AllAuthModes() []AuthMode { return authModes }

// AuthModeByID returns the auth mode with the given id, and whether it was found.
func AuthModeByID(id string) (AuthMode, bool) {
	for _, m := range authModes {
		if m.ID == id {
			return m, true
		}
	}
	return AuthMode{}, false
}
