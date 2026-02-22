package validate

import (
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
