package uiconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// staticEnv is a deterministic envLookup for tests.
func staticEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// withFakeHome relocates os.UserHomeDir() to the given dir for the
// duration of the test. Required because the loader's tier-2 fallback
// resolves the user config via os.UserHomeDir.
func withFakeHome(t *testing.T, dir string) {
	t.Helper()
	origHome, hadHome := os.LookupEnv("HOME")
	if err := os.Setenv("HOME", dir); err != nil {
		t.Fatalf("setenv HOME: %v", err)
	}
	t.Cleanup(func() {
		if hadHome {
			_ = os.Setenv("HOME", origHome)
		} else {
			_ = os.Unsetenv("HOME")
		}
	})
}

func TestLoad_WorkspaceTakesPrecedence(t *testing.T) {
	workspace := t.TempDir()
	userHome := t.TempDir()
	withFakeHome(t, userHome)

	writeFile(t, filepath.Join(workspace, ".forge", "ui.yaml"), `
skill_builder:
  provider: openai
  model: gpt-4.1
  base_url: https://workspace.example.com/v1
`)
	writeFile(t, filepath.Join(userHome, ".forge", "ui.yaml"), `
skill_builder:
  provider: anthropic
  model: claude-sonnet-4
`)

	got, err := LoadSkillBuilderLLM(workspace, "", staticEnv(map[string]string{
		"OPENAI_API_KEY": "sk-workspace",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Source != SourceWorkspace {
		t.Errorf("Source = %q, want %q", got.Source, SourceWorkspace)
	}
	if got.Provider != "openai" || got.Model != "gpt-4.1" {
		t.Errorf("workspace config not applied: %+v", got)
	}
	if got.BaseURL != "https://workspace.example.com/v1" {
		t.Errorf("BaseURL = %q, want workspace URL", got.BaseURL)
	}
	if got.APIKey != "sk-workspace" {
		t.Errorf("APIKey = %q, want resolved from default env", got.APIKey)
	}
}

func TestLoad_UserFallbackWhenNoWorkspace(t *testing.T) {
	workspace := t.TempDir()
	userHome := t.TempDir()
	withFakeHome(t, userHome)

	writeFile(t, filepath.Join(userHome, ".forge", "ui.yaml"), `
skill_builder:
  provider: anthropic
  model: claude-sonnet-4
`)

	got, err := LoadSkillBuilderLLM(workspace, "", staticEnv(map[string]string{
		"ANTHROPIC_API_KEY": "sk-user",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Source != SourceUser {
		t.Errorf("Source = %q, want %q", got.Source, SourceUser)
	}
	if got.Provider != "anthropic" || got.Model != "claude-sonnet-4" {
		t.Errorf("user config not applied: %+v", got)
	}
	if got.APIKey != "sk-user" {
		t.Errorf("APIKey = %q, want resolved from ANTHROPIC_API_KEY", got.APIKey)
	}
	if got.Warning != "" {
		t.Errorf("Warning should be empty for user-tier, got %q", got.Warning)
	}
}

func TestLoad_AgentFallbackEmitsDeprecationWarning(t *testing.T) {
	workspace := t.TempDir()
	userHome := t.TempDir()
	withFakeHome(t, userHome)

	agentDir := filepath.Join(workspace, "demo-agent")
	writeFile(t, filepath.Join(agentDir, "forge.yaml"), `
agent_id: demo-agent
model:
  provider: openai
  name: moonshotai/Kimi-K2.6
`)
	writeFile(t, filepath.Join(agentDir, ".env"), `
OPENAI_BASE_URL=https://endpoint.example.com/v1
OPENAI_API_KEY=sk-agent
`)

	got, err := LoadSkillBuilderLLM(workspace, agentDir, staticEnv(map[string]string{
		"OPENAI_API_KEY": "sk-from-env",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Source != SourceAgentFallback {
		t.Errorf("Source = %q, want %q", got.Source, SourceAgentFallback)
	}
	if got.Warning == "" {
		t.Errorf("Warning should be set for agent_fallback")
	}
	if !strings.Contains(got.Warning, "deprecated") {
		t.Errorf("Warning should mention deprecation, got %q", got.Warning)
	}
	if got.Provider != "openai" || got.Model != "moonshotai/Kimi-K2.6" {
		t.Errorf("agent fallback not applied: %+v", got)
	}
	if got.BaseURL != "https://endpoint.example.com/v1" {
		t.Errorf("BaseURL should come from agent .env, got %q", got.BaseURL)
	}
	// APIKey resolves from the envLookup (the UI process's actual env),
	// not from the agent's .env directly. We do NOT want os.Setenv-style
	// leakage from agent .env into the UI process.
	if got.APIKey != "sk-from-env" {
		t.Errorf("APIKey = %q, want value from envLookup (NOT from agent .env)", got.APIKey)
	}
}

func TestLoad_NoConfigReturnsUnset(t *testing.T) {
	workspace := t.TempDir()
	userHome := t.TempDir()
	withFakeHome(t, userHome)

	got, err := LoadSkillBuilderLLM(workspace, "", staticEnv(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Source != SourceUnset {
		t.Errorf("Source = %q, want %q", got.Source, SourceUnset)
	}
	if got.HasCredentials() {
		t.Errorf("HasCredentials should be false when nothing is configured")
	}
}

func TestLoad_OllamaHasCredentialsWithoutKey(t *testing.T) {
	workspace := t.TempDir()
	userHome := t.TempDir()
	withFakeHome(t, userHome)

	writeFile(t, filepath.Join(workspace, ".forge", "ui.yaml"), `
skill_builder:
  provider: ollama
  model: llama3
`)

	got, err := LoadSkillBuilderLLM(workspace, "", staticEnv(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.HasCredentials() {
		t.Errorf("ollama should report HasCredentials true even without API key")
	}
}

func TestLoad_APIKeyEnvOverride(t *testing.T) {
	workspace := t.TempDir()
	userHome := t.TempDir()
	withFakeHome(t, userHome)

	// Operator stores the skill-builder key under a non-default name so
	// it doesn't collide with agent runtime credentials.
	writeFile(t, filepath.Join(workspace, ".forge", "ui.yaml"), `
skill_builder:
  provider: openai
  model: gpt-4.1
  api_key_env: WORKSPACE_LLM_KEY
`)

	got, err := LoadSkillBuilderLLM(workspace, "", staticEnv(map[string]string{
		"WORKSPACE_LLM_KEY": "sk-workspace",
		"OPENAI_API_KEY":    "sk-agent-runtime", // must NOT be picked up
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.APIKey != "sk-workspace" {
		t.Errorf("APIKey = %q, want sk-workspace via custom env override", got.APIKey)
	}
	if got.APIKeyEnv != "WORKSPACE_LLM_KEY" {
		t.Errorf("APIKeyEnv = %q, want WORKSPACE_LLM_KEY", got.APIKeyEnv)
	}
}

func TestSave_RoundTripsThroughLoad(t *testing.T) {
	workspace := t.TempDir()
	userHome := t.TempDir()
	withFakeHome(t, userHome)

	cfg := SkillBuilderConfig{
		Provider: "openai",
		Model:    "gpt-4.1",
		BaseURL:  "https://endpoint.example.com/v1",
	}
	if err := SaveSkillBuilderLLM(workspace, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := LoadSkillBuilderLLM(workspace, "", staticEnv(map[string]string{
		"OPENAI_API_KEY": "sk-roundtrip",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Source != SourceWorkspace {
		t.Errorf("Source = %q, want %q", got.Source, SourceWorkspace)
	}
	if got.Provider != cfg.Provider || got.Model != cfg.Model || got.BaseURL != cfg.BaseURL {
		t.Errorf("round-trip mismatch: saved %+v, loaded %+v", cfg, got)
	}
}

func TestSave_OverwritesExistingSkillBuilderSection(t *testing.T) {
	workspace := t.TempDir()
	userHome := t.TempDir()
	withFakeHome(t, userHome)

	// Pre-populate with an old skill_builder section; Save must replace
	// it with the new one rather than appending or duplicating.
	path := filepath.Join(workspace, ".forge", "ui.yaml")
	writeFile(t, path, `
skill_builder:
  provider: anthropic
  model: claude-old
`)

	cfg := SkillBuilderConfig{Provider: "openai", Model: "gpt-4.1"}
	if err := SaveSkillBuilderLLM(workspace, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(raw), "claude-old") {
		t.Errorf("Save left old model in file:\n%s", raw)
	}
	if !strings.Contains(string(raw), "gpt-4.1") {
		t.Errorf("Save did not write new model:\n%s", raw)
	}
}

func TestValidate_RejectsBadProvider(t *testing.T) {
	cases := []struct {
		name string
		cfg  SkillBuilderConfig
		want string // substring expected in error
	}{
		{"no provider", SkillBuilderConfig{Model: "x"}, "provider is required"},
		{"unknown provider", SkillBuilderConfig{Provider: "bogus", Model: "x"}, "unknown provider"},
		{"no model", SkillBuilderConfig{Provider: "openai"}, "model is required"},
		{"base_url with non-openai", SkillBuilderConfig{Provider: "anthropic", Model: "x", BaseURL: "https://y"}, "base_url is only meaningful"},
		{"invalid api_key_env", SkillBuilderConfig{Provider: "openai", Model: "x", APIKeyEnv: "BAD KEY"}, "api_key_env"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSkillBuilderConfig(tc.cfg)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestValidate_AcceptsKnownProviders(t *testing.T) {
	for _, p := range []string{"openai", "anthropic", "gemini", "ollama"} {
		t.Run(p, func(t *testing.T) {
			if err := ValidateSkillBuilderConfig(SkillBuilderConfig{Provider: p, Model: "m"}); err != nil {
				t.Errorf("provider %q should validate, got %v", p, err)
			}
		})
	}
}
