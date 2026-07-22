package validate

import (
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/types"
)

func validConfig() *types.ForgeConfig {
	return &types.ForgeConfig{
		AgentID:    "my-agent",
		Version:    "0.1.0",
		Framework:  "langchain",
		Entrypoint: "python agent.py",
		Model: types.ModelRef{
			Provider: "openai",
			Name:     "gpt-4",
		},
		Tools: []types.ToolRef{
			{Name: "web-search", Type: "builtin"},
		},
	}
}

func TestValidateForgeConfig_Valid(t *testing.T) {
	r := ValidateForgeConfig(validConfig())
	if !r.IsValid() {
		t.Fatalf("expected valid, got errors: %v", r.Errors)
	}
	if len(r.Warnings) != 0 {
		t.Fatalf("expected no warnings, got: %v", r.Warnings)
	}
}

func hasSubstr(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// TestValidateForgeConfig_AuthScheme covers the #303-review validation:
// unknown scheme → error; apikey_header is accepted; a custom header
// colliding with a native auth header → error; auth_scheme on an
// unsupported provider and a stray auth_header_name → warnings.
func TestValidateForgeConfig_AuthScheme(t *testing.T) {
	t.Run("apikey_header is valid", func(t *testing.T) {
		cfg := validConfig()
		cfg.Model.AuthScheme = "apikey_header"
		cfg.Model.AuthHeaderName = "x-gateway-key"
		r := ValidateForgeConfig(cfg)
		if !r.IsValid() {
			t.Fatalf("expected valid, got errors: %v", r.Errors)
		}
	})

	t.Run("apikey_header_only is valid and honors auth_header_name", func(t *testing.T) {
		cfg := validConfig()
		cfg.Model.AuthScheme = "apikey_header_only"
		cfg.Model.AuthHeaderName = "x-gateway-key"
		r := ValidateForgeConfig(cfg)
		if !r.IsValid() {
			t.Fatalf("expected valid, got errors: %v", r.Errors)
		}
		if hasSubstr(r.Warnings, "auth_header_name is set but auth_scheme") {
			t.Errorf("auth_header_name must apply to apikey_header_only, got warnings=%v", r.Warnings)
		}
	})

	t.Run("apikey_header_only header collision errors", func(t *testing.T) {
		cfg := validConfig()
		cfg.Model.AuthScheme = "apikey_header_only"
		cfg.Model.AuthHeaderName = "x-api-key" // collides with native
		r := ValidateForgeConfig(cfg)
		if r.IsValid() || !hasSubstr(r.Errors, "collides with a native auth header") {
			t.Fatalf("expected a collision error, got errors=%v", r.Errors)
		}
	})

	t.Run("unknown scheme errors", func(t *testing.T) {
		cfg := validConfig()
		cfg.Model.AuthScheme = "apikey_headr" // typo
		r := ValidateForgeConfig(cfg)
		if r.IsValid() || !hasSubstr(r.Errors, "auth_scheme") {
			t.Fatalf("expected an auth_scheme error, got errors=%v", r.Errors)
		}
	})

	t.Run("header collision errors", func(t *testing.T) {
		cfg := validConfig()
		cfg.Model.AuthScheme = "apikey_header"
		cfg.Model.AuthHeaderName = "Authorization" // case-insensitive collision
		r := ValidateForgeConfig(cfg)
		if r.IsValid() || !hasSubstr(r.Errors, "collides with a native auth header") {
			t.Fatalf("expected a collision error, got errors=%v", r.Errors)
		}
	})

	t.Run("scheme on unsupported provider warns", func(t *testing.T) {
		cfg := validConfig()
		cfg.Model.Provider = "gemini"
		cfg.Model.AuthScheme = "apikey_header"
		r := ValidateForgeConfig(cfg)
		if !r.IsValid() || !hasSubstr(r.Warnings, "only affects the openai and anthropic clients") {
			t.Fatalf("expected a provider warning and no error; errors=%v warnings=%v", r.Errors, r.Warnings)
		}
	})

	t.Run("stray auth_header_name warns", func(t *testing.T) {
		cfg := validConfig()
		cfg.Model.AuthHeaderName = "apikey" // no apikey_header scheme
		r := ValidateForgeConfig(cfg)
		if !r.IsValid() || !hasSubstr(r.Warnings, "auth_header_name is set but auth_scheme") {
			t.Fatalf("expected a stray-header warning and no error; errors=%v warnings=%v", r.Errors, r.Warnings)
		}
	})
}

func TestValidateForgeConfig_InvalidAgentID(t *testing.T) {
	cfg := validConfig()
	cfg.AgentID = "My_Agent!"
	r := ValidateForgeConfig(cfg)
	if r.IsValid() {
		t.Fatal("expected invalid")
	}
	if len(r.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d: %v", len(r.Errors), r.Errors)
	}
}

func TestValidateForgeConfig_EmptyAgentID(t *testing.T) {
	cfg := validConfig()
	cfg.AgentID = ""
	r := ValidateForgeConfig(cfg)
	if r.IsValid() {
		t.Fatal("expected invalid")
	}
}

func TestValidateForgeConfig_BadSemver(t *testing.T) {
	cfg := validConfig()
	cfg.Version = "v1.0"
	r := ValidateForgeConfig(cfg)
	if r.IsValid() {
		t.Fatal("expected invalid")
	}
}

func TestValidateForgeConfig_EmptyEntrypoint(t *testing.T) {
	cfg := validConfig()
	cfg.Entrypoint = ""
	r := ValidateForgeConfig(cfg)
	if r.IsValid() {
		t.Fatal("expected invalid")
	}
}

func TestValidateForgeConfig_EmptyToolName(t *testing.T) {
	cfg := validConfig()
	cfg.Tools = []types.ToolRef{{Name: ""}}
	r := ValidateForgeConfig(cfg)
	if r.IsValid() {
		t.Fatal("expected invalid")
	}
}

func TestValidateForgeConfig_ProviderWithoutName(t *testing.T) {
	cfg := validConfig()
	cfg.Model = types.ModelRef{Provider: "openai", Name: ""}
	r := ValidateForgeConfig(cfg)
	if !r.IsValid() {
		t.Fatalf("expected valid, got errors: %v", r.Errors)
	}
	if len(r.Warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d: %v", len(r.Warnings), r.Warnings)
	}
}

func TestValidateForgeConfig_UnknownFramework(t *testing.T) {
	cfg := validConfig()
	cfg.Framework = "autogen"
	r := ValidateForgeConfig(cfg)
	if !r.IsValid() {
		t.Fatalf("expected valid, got errors: %v", r.Errors)
	}
	if len(r.Warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d: %v", len(r.Warnings), r.Warnings)
	}
}

func TestValidateForgeConfig_OrgIDOnNonOpenAI(t *testing.T) {
	cfg := validConfig()
	cfg.Model.Provider = "anthropic"
	cfg.Model.OrganizationID = "org-test-123"
	r := ValidateForgeConfig(cfg)
	if !r.IsValid() {
		t.Fatalf("expected valid, got errors: %v", r.Errors)
	}
	found := false
	for _, w := range r.Warnings {
		if len(w) > 0 && w[0:5] == "model" {
			found = true
		}
	}
	if !found {
		t.Error("expected warning about organization_id on non-openai provider")
	}
}

func TestValidateForgeConfig_OrgIDOnOpenAI(t *testing.T) {
	cfg := validConfig()
	cfg.Model.Provider = "openai"
	cfg.Model.OrganizationID = "org-test-123"
	r := ValidateForgeConfig(cfg)
	if !r.IsValid() {
		t.Fatalf("expected valid, got errors: %v", r.Errors)
	}
	// Should NOT produce a warning for openai
	for _, w := range r.Warnings {
		if len(w) > 18 && w[:18] == "model.organization" {
			t.Errorf("unexpected warning for openai: %s", w)
		}
	}
}
