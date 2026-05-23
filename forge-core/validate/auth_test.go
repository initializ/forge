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
