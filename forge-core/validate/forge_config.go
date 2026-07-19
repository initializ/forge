package validate

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/initializ/forge/forge-core/llm"
	"github.com/initializ/forge/forge-core/scheduler"
	"github.com/initializ/forge/forge-core/types"
)

var kebabCasePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// knownModelAuthSchemes is the accepted set for model.auth_scheme (outbound
// LLM auth). "" / x_api_key / bearer all resolve to the provider-native
// header; aws_sigv4 (#202) and apikey_header (#302) are the active schemes.
var knownModelAuthSchemes = map[string]bool{
	"":                         true,
	"x_api_key":                true,
	"bearer":                   true,
	llm.AuthSchemeAWSSigV4:     true,
	llm.AuthSchemeAPIKeyHeader: true,
}

// nativeAuthHeaders are the provider-native auth headers apikey_header must
// not overwrite (case-insensitive).
var nativeAuthHeaders = map[string]bool{"authorization": true, "x-api-key": true}

var (
	agentIDPattern = regexp.MustCompile(`^[a-z0-9-]+$`)
	semverPattern  = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

	knownFrameworks      = map[string]bool{"forge": true, "crewai": true, "langchain": true, "custom": true}
	knownEgressProfiles  = map[string]bool{"strict": true, "standard": true, "permissive": true}
	knownEgressModes     = map[string]bool{"deny-all": true, "allowlist": true, "dev-open": true}
	knownSecretProviders = map[string]bool{"env": true, "encrypted-file": true}
	knownGuardrailTypes  = map[string]bool{
		"no_pii":                   true,
		"jailbreak_protection":     true,
		"tool_scope_enforcement":   true,
		"output_format_validation": true,
		"content_filter":           true,
	}
)

// ValidationResult holds errors and warnings from config validation.
type ValidationResult struct {
	Errors   []string
	Warnings []string
}

// IsValid returns true if there are no validation errors.
func (r *ValidationResult) IsValid() bool {
	return len(r.Errors) == 0
}

// ValidateForgeConfig checks a ForgeConfig for errors and warnings.
func ValidateForgeConfig(cfg *types.ForgeConfig) *ValidationResult {
	r := &ValidationResult{}

	if cfg.AgentID == "" {
		r.Errors = append(r.Errors, "agent_id is required")
	} else if !agentIDPattern.MatchString(cfg.AgentID) {
		r.Errors = append(r.Errors, fmt.Sprintf("agent_id %q must match ^[a-z0-9-]+$", cfg.AgentID))
	}

	if cfg.Version == "" {
		r.Errors = append(r.Errors, "version is required")
	} else if !semverPattern.MatchString(cfg.Version) {
		r.Errors = append(r.Errors, fmt.Sprintf("version %q is not valid semver", cfg.Version))
	}

	if cfg.Entrypoint == "" && cfg.Framework != "forge" {
		r.Errors = append(r.Errors, "entrypoint is required")
	}

	for i, t := range cfg.Tools {
		if t.Name == "" {
			r.Errors = append(r.Errors, fmt.Sprintf("tools[%d]: name is required", i))
		}
	}

	if cfg.Model.Provider != "" && cfg.Model.Name == "" {
		r.Warnings = append(r.Warnings, "model.provider is set but model.name is empty")
	}

	if cfg.Model.OrganizationID != "" && cfg.Model.Provider != "" && cfg.Model.Provider != "openai" {
		r.Warnings = append(r.Warnings, fmt.Sprintf("model.organization_id is set but provider is %q (only used by openai)", cfg.Model.Provider))
	}

	// model.auth_scheme validation (#202 / #302). An unrecognized value
	// silently degrades to native-headers-only — reproducing the exact 401
	// the apikey_header scheme exists to fix — so reject it here.
	if s := cfg.Model.AuthScheme; !knownModelAuthSchemes[s] {
		r.Errors = append(r.Errors, fmt.Sprintf("model.auth_scheme %q is not recognized (known: x_api_key, bearer, aws_sigv4, apikey_header)", s))
	}
	// Only the openai and anthropic clients honor auth_scheme; warn if it's
	// set on a provider that will silently ignore it.
	if s := cfg.Model.AuthScheme; (s == llm.AuthSchemeAWSSigV4 || s == llm.AuthSchemeAPIKeyHeader) &&
		cfg.Model.Provider != "" && cfg.Model.Provider != "openai" && cfg.Model.Provider != "anthropic" {
		r.Warnings = append(r.Warnings, fmt.Sprintf("model.auth_scheme %q only affects the openai and anthropic clients; provider %q ignores it", s, cfg.Model.Provider))
	}
	// apikey_header's custom header must not collide with a native auth
	// header, or it would overwrite the provider's Bearer / x-api-key with
	// the raw key — breaking auth in a maximally confusing way (#303 review).
	if cfg.Model.AuthScheme == llm.AuthSchemeAPIKeyHeader && cfg.Model.AuthHeaderName != "" &&
		nativeAuthHeaders[strings.ToLower(cfg.Model.AuthHeaderName)] {
		r.Errors = append(r.Errors, fmt.Sprintf("model.auth_header_name %q collides with a native auth header; choose a distinct gateway header (e.g. apikey, x-gateway-key)", cfg.Model.AuthHeaderName))
	}
	// auth_header_name only applies to apikey_header.
	if cfg.Model.AuthHeaderName != "" && cfg.Model.AuthScheme != llm.AuthSchemeAPIKeyHeader {
		r.Warnings = append(r.Warnings, `model.auth_header_name is set but auth_scheme is not "apikey_header"; it will be ignored`)
	}

	if cfg.Framework != "" && !knownFrameworks[cfg.Framework] {
		r.Warnings = append(r.Warnings, fmt.Sprintf("unknown framework %q (known: forge, crewai, langchain)", cfg.Framework))
	}

	// Validate egress config
	if cfg.Egress.Profile != "" && !knownEgressProfiles[cfg.Egress.Profile] {
		r.Errors = append(r.Errors, fmt.Sprintf("egress.profile %q must be one of: strict, standard, permissive", cfg.Egress.Profile))
	}
	if cfg.Egress.Mode != "" && !knownEgressModes[cfg.Egress.Mode] {
		r.Errors = append(r.Errors, fmt.Sprintf("egress.mode %q must be one of: deny-all, allowlist, dev-open", cfg.Egress.Mode))
	}
	if cfg.Egress.Mode == "dev-open" {
		r.Warnings = append(r.Warnings, "egress mode 'dev-open' is not recommended for production")
	}

	// Validate secrets config
	for _, p := range cfg.Secrets.Providers {
		if !knownSecretProviders[p] {
			r.Warnings = append(r.Warnings, fmt.Sprintf("unknown secret provider %q (known: env, encrypted-file)", p))
		}
	}

	// Validate schedules config
	seenScheduleIDs := make(map[string]bool, len(cfg.Schedules))
	for i, s := range cfg.Schedules {
		if s.ID == "" {
			r.Errors = append(r.Errors, fmt.Sprintf("schedules[%d]: id is required", i))
		} else if !kebabCasePattern.MatchString(s.ID) {
			r.Errors = append(r.Errors, fmt.Sprintf("schedules[%d]: id %q must be kebab-case", i, s.ID))
		} else if seenScheduleIDs[s.ID] {
			r.Errors = append(r.Errors, fmt.Sprintf("schedules[%d]: duplicate id %q", i, s.ID))
		} else {
			seenScheduleIDs[s.ID] = true
		}

		if s.Cron == "" {
			r.Errors = append(r.Errors, fmt.Sprintf("schedules[%d]: cron is required", i))
		} else if _, err := scheduler.Parse(s.Cron); err != nil {
			r.Errors = append(r.Errors, fmt.Sprintf("schedules[%d]: invalid cron %q: %s", i, s.Cron, err))
		}

		if s.Task == "" {
			r.Errors = append(r.Errors, fmt.Sprintf("schedules[%d]: task is required", i))
		}
	}

	ValidateAuthConfig(cfg.Auth, r)
	ValidateMCPConfig(cfg.MCP, r)
	validateStandaloneDelegatedConsent(cfg, r)

	return r
}

// validateStandaloneDelegatedConsent enforces the standalone (#332) rules for
// auth.type=user servers that have NO platform block: without a platform token
// endpoint, Forge runs the per-user OAuth itself, which needs explicit
// endpoints + client_id (no runtime discovery) and the authorization_code
// grant. Lives here (not in ValidateMCPConfig) because the standalone-vs-managed
// distinction depends on the top-level platform block. Mirrors NewServer.
func validateStandaloneDelegatedConsent(cfg *types.ForgeConfig, r *ValidationResult) {
	managed := cfg.Platform != nil && cfg.Platform.TokenEndpoint != ""
	if managed {
		return
	}
	for i, s := range cfg.MCP.Servers {
		if s.Auth == nil || s.Auth.Type != "user" {
			continue
		}
		prefix := fmt.Sprintf("mcp.servers[%d] (%s)", i, s.Name)
		if s.Auth.Grant != "" && s.Auth.Grant != "authorization_code" {
			r.Errors = append(r.Errors, prefix+": standalone auth.type=user (no platform block) uses the authorization_code grant — remove grant or set it to authorization_code")
		}
		if s.Auth.AuthorizeURL == "" || s.Auth.TokenURL == "" || s.Auth.ClientID == "" {
			r.Errors = append(r.Errors, prefix+": standalone auth.type=user requires explicit auth.authorize_url, auth.token_url, and auth.client_id (no runtime discovery) — or add a platform block for managed delegation")
		}
	}
}
