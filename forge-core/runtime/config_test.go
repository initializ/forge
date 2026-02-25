package runtime

import (
	"testing"

	"github.com/initializ/forge/forge-core/types"
)

func TestResolveModelConfig_FallbacksFromYAML(t *testing.T) {
	cfg := &types.ForgeConfig{
		Model: types.ModelRef{
			Provider: "anthropic",
			Name:     "claude-sonnet-4-20250514",
			Fallbacks: []types.ModelFallback{
				{Provider: "openai", Name: "gpt-4o"},
				{Provider: "gemini"},
			},
		},
	}
	envVars := map[string]string{
		"ANTHROPIC_API_KEY": "sk-ant-test",
		"OPENAI_API_KEY":    "sk-openai-test",
		"GEMINI_API_KEY":    "gemini-test",
	}

	mc := ResolveModelConfig(cfg, envVars, "")
	if mc == nil {
		t.Fatal("expected non-nil ModelConfig")
	}
	if mc.Provider != "anthropic" {
		t.Fatalf("expected primary provider anthropic, got %s", mc.Provider)
	}
	if len(mc.Fallbacks) != 2 {
		t.Fatalf("expected 2 fallbacks, got %d", len(mc.Fallbacks))
	}
	if mc.Fallbacks[0].Provider != "openai" {
		t.Errorf("expected first fallback openai, got %s", mc.Fallbacks[0].Provider)
	}
	if mc.Fallbacks[0].Client.Model != "gpt-4o" {
		t.Errorf("expected first fallback model gpt-4o, got %s", mc.Fallbacks[0].Client.Model)
	}
	if mc.Fallbacks[1].Provider != "gemini" {
		t.Errorf("expected second fallback gemini, got %s", mc.Fallbacks[1].Provider)
	}
	if mc.Fallbacks[1].Client.Model != "gemini-2.5-flash" {
		t.Errorf("expected default gemini model, got %s", mc.Fallbacks[1].Client.Model)
	}
}

func TestResolveModelConfig_FallbacksFromEnvVar(t *testing.T) {
	cfg := &types.ForgeConfig{
		Model: types.ModelRef{
			Provider: "anthropic",
			Name:     "claude-sonnet-4-20250514",
		},
	}
	envVars := map[string]string{
		"ANTHROPIC_API_KEY":     "sk-ant-test",
		"OPENAI_API_KEY":        "sk-openai-test",
		"GEMINI_API_KEY":        "gemini-test",
		"FORGE_MODEL_FALLBACKS": "openai:gpt-4o-mini,gemini:gemini-2.5-pro",
	}

	mc := ResolveModelConfig(cfg, envVars, "")
	if mc == nil {
		t.Fatal("expected non-nil ModelConfig")
	}
	if len(mc.Fallbacks) < 2 {
		t.Fatalf("expected at least 2 fallbacks, got %d", len(mc.Fallbacks))
	}
	if mc.Fallbacks[0].Provider != "openai" {
		t.Errorf("expected first fallback openai, got %s", mc.Fallbacks[0].Provider)
	}
	if mc.Fallbacks[0].Client.Model != "gpt-4o-mini" {
		t.Errorf("expected model gpt-4o-mini, got %s", mc.Fallbacks[0].Client.Model)
	}
	if mc.Fallbacks[1].Provider != "gemini" {
		t.Errorf("expected second fallback gemini, got %s", mc.Fallbacks[1].Provider)
	}
	if mc.Fallbacks[1].Client.Model != "gemini-2.5-pro" {
		t.Errorf("expected model gemini-2.5-pro, got %s", mc.Fallbacks[1].Client.Model)
	}
}

func TestResolveModelConfig_AutoDetectFallbacks(t *testing.T) {
	cfg := &types.ForgeConfig{
		Model: types.ModelRef{
			Provider: "anthropic",
			Name:     "claude-sonnet-4-20250514",
		},
	}
	envVars := map[string]string{
		"ANTHROPIC_API_KEY": "sk-ant-test",
		"OPENAI_API_KEY":    "sk-openai-test",
	}

	mc := ResolveModelConfig(cfg, envVars, "")
	if mc == nil {
		t.Fatal("expected non-nil ModelConfig")
	}
	if len(mc.Fallbacks) != 1 {
		t.Fatalf("expected 1 auto-detected fallback, got %d", len(mc.Fallbacks))
	}
	if mc.Fallbacks[0].Provider != "openai" {
		t.Errorf("expected auto-detected fallback openai, got %s", mc.Fallbacks[0].Provider)
	}
}

func TestResolveModelConfig_PrimaryNotDuplicated(t *testing.T) {
	cfg := &types.ForgeConfig{
		Model: types.ModelRef{
			Provider: "openai",
			Name:     "gpt-4o",
			Fallbacks: []types.ModelFallback{
				{Provider: "openai", Name: "gpt-4o-mini"},
			},
		},
	}
	envVars := map[string]string{
		"OPENAI_API_KEY": "sk-openai-test",
	}

	mc := ResolveModelConfig(cfg, envVars, "")
	if mc == nil {
		t.Fatal("expected non-nil ModelConfig")
	}
	// Primary provider should not appear in fallbacks
	for _, fb := range mc.Fallbacks {
		if fb.Provider == "openai" {
			t.Errorf("primary provider openai should not appear in fallbacks")
		}
	}
}

func TestResolveModelConfig_MissingAPIKeySkipsFallback(t *testing.T) {
	cfg := &types.ForgeConfig{
		Model: types.ModelRef{
			Provider: "anthropic",
			Name:     "claude-sonnet-4-20250514",
			Fallbacks: []types.ModelFallback{
				{Provider: "openai", Name: "gpt-4o"},
			},
		},
	}
	envVars := map[string]string{
		"ANTHROPIC_API_KEY": "sk-ant-test",
		// No OPENAI_API_KEY
	}

	mc := ResolveModelConfig(cfg, envVars, "")
	if mc == nil {
		t.Fatal("expected non-nil ModelConfig")
	}
	if len(mc.Fallbacks) != 0 {
		t.Fatalf("expected 0 fallbacks (missing API key), got %d", len(mc.Fallbacks))
	}
}

func TestResolveModelConfig_NoFallbacksWhenSingleProvider(t *testing.T) {
	cfg := &types.ForgeConfig{
		Model: types.ModelRef{
			Provider: "anthropic",
			Name:     "claude-sonnet-4-20250514",
		},
	}
	envVars := map[string]string{
		"ANTHROPIC_API_KEY": "sk-ant-test",
	}

	mc := ResolveModelConfig(cfg, envVars, "")
	if mc == nil {
		t.Fatal("expected non-nil ModelConfig")
	}
	if len(mc.Fallbacks) != 0 {
		t.Fatalf("expected 0 fallbacks, got %d", len(mc.Fallbacks))
	}
}

func TestDefaultModelForProvider(t *testing.T) {
	tests := []struct {
		provider string
		expected string
	}{
		{"openai", "gpt-5.2-2025-12-11"},
		{"anthropic", "claude-sonnet-4-20250514"},
		{"gemini", "gemini-2.5-flash"},
		{"ollama", "llama3"},
		{"unknown", ""},
	}
	for _, tt := range tests {
		got := defaultModelForProvider(tt.provider)
		if got != tt.expected {
			t.Errorf("defaultModelForProvider(%q) = %q, want %q", tt.provider, got, tt.expected)
		}
	}
}
