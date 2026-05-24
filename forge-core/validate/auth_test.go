package validate

import (
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/types"
)

func TestValidateAuthConfig_EmptyIsValid(t *testing.T) {
	r := &ValidationResult{}
	ValidateAuthConfig(types.AuthConfig{}, r)
	if !r.IsValid() {
		t.Errorf("empty AuthConfig should be valid, got errors: %v", r.Errors)
	}
}

func TestValidateAuthConfig_RequiredWithoutProviders(t *testing.T) {
	r := &ValidationResult{}
	ValidateAuthConfig(types.AuthConfig{Required: true}, r)
	if r.IsValid() {
		t.Error("required=true with no providers should be an error")
	}
	if !containsSubstr(r.Errors, "required") {
		t.Errorf("missing 'required' in error message: %v", r.Errors)
	}
}

func TestValidateAuthConfig_HTTPVerifier(t *testing.T) {
	tests := []struct {
		name     string
		provider types.AuthProvider
		wantErr  string
	}{
		{
			name: "valid",
			provider: types.AuthProvider{
				Type:     "http_verifier",
				Settings: map[string]any{"url": "https://verify.example.com"},
			},
		},
		{
			name: "missing url",
			provider: types.AuthProvider{
				Type:     "http_verifier",
				Settings: map[string]any{},
			},
			wantErr: "settings.url is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &ValidationResult{}
			ValidateAuthConfig(types.AuthConfig{Providers: []types.AuthProvider{tt.provider}}, r)
			if tt.wantErr == "" {
				if !r.IsValid() {
					t.Errorf("want valid, got errors: %v", r.Errors)
				}
			} else {
				if !containsSubstr(r.Errors, tt.wantErr) {
					t.Errorf("want error containing %q, got %v", tt.wantErr, r.Errors)
				}
			}
		})
	}
}

func TestValidateAuthConfig_OIDC(t *testing.T) {
	tests := []struct {
		name     string
		settings map[string]any
		wantErr  string
	}{
		{
			name: "valid",
			settings: map[string]any{
				"issuer":   "https://login.example.com",
				"audience": "api://forge",
			},
		},
		{
			name:     "missing issuer",
			settings: map[string]any{"audience": "api://forge"},
			wantErr:  "settings.issuer is required",
		},
		{
			name:     "missing audience",
			settings: map[string]any{"issuer": "https://login.example.com"},
			wantErr:  "settings.audience is required",
		},
		{
			name:     "both missing",
			settings: map[string]any{},
			wantErr:  "issuer is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &ValidationResult{}
			ValidateAuthConfig(types.AuthConfig{
				Providers: []types.AuthProvider{{Type: "oidc", Settings: tt.settings}},
			}, r)
			if tt.wantErr == "" {
				if !r.IsValid() {
					t.Errorf("want valid, got errors: %v", r.Errors)
				}
			} else {
				if !containsSubstr(r.Errors, tt.wantErr) {
					t.Errorf("want error containing %q, got %v", tt.wantErr, r.Errors)
				}
			}
		})
	}
}

func TestValidateAuthConfig_StaticToken(t *testing.T) {
	tests := []struct {
		name     string
		settings map[string]any
		wantErr  string
		wantWarn string
	}{
		{
			name:     "valid with token_env",
			settings: map[string]any{"token_env": "FORGE_TOKEN"},
		},
		{
			name:     "valid with token (warns)",
			settings: map[string]any{"token": "literal-secret"},
			wantWarn: "footgun",
		},
		{
			name:     "neither token nor token_env",
			settings: map[string]any{},
			wantErr:  "settings.token or settings.token_env is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &ValidationResult{}
			ValidateAuthConfig(types.AuthConfig{
				Providers: []types.AuthProvider{{Type: "static_token", Settings: tt.settings}},
			}, r)
			if tt.wantErr != "" && !containsSubstr(r.Errors, tt.wantErr) {
				t.Errorf("want error containing %q, got %v", tt.wantErr, r.Errors)
			}
			if tt.wantWarn != "" && !containsSubstr(r.Warnings, tt.wantWarn) {
				t.Errorf("want warning containing %q, got %v", tt.wantWarn, r.Warnings)
			}
		})
	}
}

func TestValidateAuthConfig_UnknownType(t *testing.T) {
	r := &ValidationResult{}
	ValidateAuthConfig(types.AuthConfig{
		Providers: []types.AuthProvider{{Type: "ldap"}},
	}, r)
	if r.IsValid() {
		t.Error("unknown provider type should fail validation")
	}
	if !containsSubstr(r.Errors, "unknown type") {
		t.Errorf("want 'unknown type' in errors, got %v", r.Errors)
	}
}

func TestValidateAuthConfig_MissingType(t *testing.T) {
	r := &ValidationResult{}
	ValidateAuthConfig(types.AuthConfig{
		Providers: []types.AuthProvider{{Settings: map[string]any{"url": "https://x"}}},
	}, r)
	if r.IsValid() {
		t.Error("provider without type should fail validation")
	}
}

func TestValidateAuthConfig_DuplicateNamesWarn(t *testing.T) {
	r := &ValidationResult{}
	ValidateAuthConfig(types.AuthConfig{
		Providers: []types.AuthProvider{
			{Type: "oidc", Name: "sso", Settings: map[string]any{"issuer": "https://x", "audience": "y"}},
			{Type: "oidc", Name: "sso", Settings: map[string]any{"issuer": "https://x", "audience": "y"}},
		},
	}, r)
	if !containsSubstr(r.Warnings, "duplicate name") {
		t.Errorf("want duplicate-name warning, got %v", r.Warnings)
	}
}

func TestValidateForgeConfig_IncludesAuth(t *testing.T) {
	// Ensure the top-level ValidateForgeConfig calls ValidateAuthConfig.
	cfg := &types.ForgeConfig{
		AgentID: "test-agent",
		Version: "0.1.0",
		Auth: types.AuthConfig{
			Providers: []types.AuthProvider{
				{Type: "oidc"}, // missing issuer + audience
			},
		},
	}
	r := ValidateForgeConfig(cfg)
	if r.IsValid() {
		t.Fatal("config with invalid oidc settings should fail validation")
	}
	if !containsSubstr(r.Errors, "issuer is required") {
		t.Errorf("want oidc-issuer error to surface via ValidateForgeConfig, got %v", r.Errors)
	}
}

// containsSubstr returns true if any string in ss contains substr.
func containsSubstr(ss []string, substr string) bool {
	for _, s := range ss {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

// --- Review M5: unknown-key warning + filter helper ---

func TestValidateAuthConfig_WarnsOnUnknownSettingsKey(t *testing.T) {
	// Typo: `aud` instead of `audience`. The required-keys check would
	// fire (audience missing), but we ALSO want a loud warning about
	// the unknown key so operators can spot the actual typo, not just
	// the symptom.
	cfg := types.AuthConfig{
		Required: true,
		Providers: []types.AuthProvider{{
			Type: "oidc",
			Settings: map[string]any{
				"issuer":   "https://login.example.com",
				"aud":      "api://forge", // typo: should be 'audience'
				"audience": "api://forge", // keep the required-check passing
			},
		}},
	}
	result := &ValidationResult{}
	ValidateAuthConfig(cfg, result)
	if !containsSubstr(result.Warnings, `unknown settings key "aud"`) {
		t.Errorf("expected warning about unknown 'aud' key, got %v", result.Warnings)
	}
}

func TestValidateAuthConfig_NoWarningForKnownKeys(t *testing.T) {
	// Every documented oidc key should be silent.
	cfg := types.AuthConfig{
		Required: true,
		Providers: []types.AuthProvider{{
			Type: "oidc",
			Settings: map[string]any{
				"issuer":         "https://x",
				"audience":       "y",
				"client_id":      "c",
				"jwks_url":       "https://x/jwks",
				"jwks_cache_ttl": "1h",
				"clock_skew":     "30s",
				"claim_map":      map[string]any{"groups": "roles"},
			},
		}},
	}
	result := &ValidationResult{}
	ValidateAuthConfig(cfg, result)
	for _, w := range result.Warnings {
		if strings.Contains(w, "unknown settings key") {
			t.Errorf("unexpected unknown-key warning for known oidc field: %q", w)
		}
	}
}

func TestFilterKnownSettings_DropsUnknownKeys(t *testing.T) {
	// Defense-in-depth filter that forge-ui's handler runs on
	// untrusted Web UI input. Unknown keys must NOT survive.
	in := map[string]any{
		"audience": "api://forge",
		"issuer":   "https://x",
		"evil_key": "attacker-value", // unknown for oidc
	}
	out := FilterKnownSettings("oidc", in)
	if out["audience"] != "api://forge" {
		t.Errorf("audience dropped: %v", out)
	}
	if out["issuer"] != "https://x" {
		t.Errorf("issuer dropped: %v", out)
	}
	if _, exists := out["evil_key"]; exists {
		t.Error("evil_key must be filtered out for oidc settings")
	}
}

func TestFilterKnownSettings_UnknownProviderTypePassthrough(t *testing.T) {
	// If the provider type isn't in the whitelist, pass through —
	// validateProviderSettings' "unknown type" error catches that case
	// separately.
	in := map[string]any{"x": "y"}
	out := FilterKnownSettings("future_provider", in)
	if out["x"] != "y" {
		t.Errorf("unknown provider type should passthrough, got %v", out)
	}
}

func TestFilterKnownSettings_AllPhase2Providers(t *testing.T) {
	cases := []struct {
		provider string
		good     string // a key that SHOULD survive
	}{
		{"aws_sigv4", "region"},
		{"aws_sigv4", "allowed_accounts"},
		{"aws_sigv4", "sts_endpoint"}, // test-only override, but YAML-reachable
		{"gcp_iap", "audience"},
		{"azure_ad", "tenant_id"},
		{"azure_ad", "allowed_tenants"},
		{"azure_ad", "allow_multi_tenant"},
	}
	for _, tc := range cases {
		t.Run(tc.provider+"/"+tc.good, func(t *testing.T) {
			out := FilterKnownSettings(tc.provider, map[string]any{
				tc.good:  "x",
				"evil_X": "bad",
			})
			if _, exists := out[tc.good]; !exists {
				t.Errorf("%s: known key %q was dropped", tc.provider, tc.good)
			}
			if _, exists := out["evil_X"]; exists {
				t.Errorf("%s: unknown key 'evil_X' survived filter", tc.provider)
			}
		})
	}
}
