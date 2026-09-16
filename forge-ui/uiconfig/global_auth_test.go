package uiconfig

import (
	"path/filepath"
	"testing"

	"github.com/initializ/forge/forge-core/settings"
)

// stubOAuth overrides the package-level OAuth credential checker for the
// duration of a test so resolution can be exercised without touching the
// real ~/.forge credential store.
func stubOAuth(t *testing.T, fn func(string) bool) {
	t.Helper()
	orig := oauthCredChecker
	oauthCredChecker = fn
	t.Cleanup(func() { oauthCredChecker = orig })
}

// stubGateway overrides the settings gateway checker + model-default reader
// so gateway resolution is exercised without writing settings.json files.
func stubGateway(t *testing.T, has func(workspaceDir, provider string) bool, def *settings.ModelDefault) {
	t.Helper()
	origGW, origDef := gatewayChecker, settingsModelDefault
	gatewayChecker = has
	settingsModelDefault = func(string) *settings.ModelDefault { return def }
	t.Cleanup(func() { gatewayChecker = origGW; settingsModelDefault = origDef })
}

// A workspace config that names provider+model but resolves no API key
// should pick up a machine-global OAuth token (gap-fill), keeping its
// workspace Source but flipping UseOAuth on.
func TestLoad_GlobalOAuthFillsGapInWorkspaceConfig(t *testing.T) {
	workspace := t.TempDir()
	withFakeHome(t, t.TempDir())
	stubOAuth(t, func(p string) bool { return p == "openai" })

	writeFile(t, filepath.Join(workspace, ".forge", "ui.yaml"), `
skill_builder:
  provider: openai
  model: gpt-5.4
`)

	got, err := LoadSkillBuilderLLM(workspace, "", staticEnv(map[string]string{}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Source != SourceWorkspace {
		t.Errorf("Source = %q, want %q (config still drives provider/model)", got.Source, SourceWorkspace)
	}
	if !got.UseOAuth {
		t.Errorf("UseOAuth = false, want true (OAuth gap-fill)")
	}
	if got.APIKey != "" {
		t.Errorf("APIKey = %q, want empty (token loaded at request time)", got.APIKey)
	}
	if !got.HasCredentials() {
		t.Errorf("HasCredentials = false, want true")
	}
}

// When the config already resolves an explicit API key, gap-fill must NOT
// flip to OAuth (the operator's explicit key wins in default mode).
func TestLoad_GlobalOAuthDoesNotOverrideExplicitKey(t *testing.T) {
	workspace := t.TempDir()
	withFakeHome(t, t.TempDir())
	stubOAuth(t, func(p string) bool { return true })

	writeFile(t, filepath.Join(workspace, ".forge", "ui.yaml"), `
skill_builder:
  provider: openai
  model: gpt-5.4
`)

	got, err := LoadSkillBuilderLLM(workspace, "", staticEnv(map[string]string{
		"OPENAI_API_KEY": "sk-explicit",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.UseOAuth {
		t.Errorf("UseOAuth = true, want false (explicit key should win)")
	}
	if got.APIKey != "sk-explicit" {
		t.Errorf("APIKey = %q, want sk-explicit", got.APIKey)
	}
}

// use_global_auth forces the global credential even when an explicit key
// resolves — the managed "always use my global login" toggle.
func TestLoad_UseGlobalAuthForcesOAuth(t *testing.T) {
	workspace := t.TempDir()
	withFakeHome(t, t.TempDir())
	stubOAuth(t, func(p string) bool { return p == "openai" })

	writeFile(t, filepath.Join(workspace, ".forge", "ui.yaml"), `
skill_builder:
  provider: openai
  model: gpt-5.4
  use_global_auth: true
`)

	got, err := LoadSkillBuilderLLM(workspace, "", staticEnv(map[string]string{
		"OPENAI_API_KEY": "sk-explicit",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.UseOAuth {
		t.Errorf("UseOAuth = false, want true (use_global_auth forces OAuth over the explicit key)")
	}
	if !got.UseGlobalAuth {
		t.Errorf("UseGlobalAuth = false, want true (round-tripped for the UI)")
	}
}

// A custom api_key_env that is empty should still be gap-filled from the
// provider's conventional global env var, keeping the config Source.
func TestLoad_GlobalEnvFillsGapForCustomEnvName(t *testing.T) {
	workspace := t.TempDir()
	withFakeHome(t, t.TempDir())
	stubOAuth(t, func(p string) bool { return false }) // no OAuth — env path

	writeFile(t, filepath.Join(workspace, ".forge", "ui.yaml"), `
skill_builder:
  provider: openai
  model: gpt-5.4
  api_key_env: CUSTOM_UNSET_KEY
`)

	got, err := LoadSkillBuilderLLM(workspace, "", staticEnv(map[string]string{
		"OPENAI_API_KEY": "sk-global",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.APIKey != "sk-global" {
		t.Errorf("APIKey = %q, want sk-global (global env gap-fill)", got.APIKey)
	}
	if got.APIKeyEnv != "OPENAI_API_KEY" {
		t.Errorf("APIKeyEnv = %q, want OPENAI_API_KEY (switched to the resolvable global var)", got.APIKeyEnv)
	}
}

// No ui.yaml + no agent + a stored OAuth token => global-OAuth default,
// the primary path for the AI agent builder.
func TestLoad_GlobalDefaultOAuthWhenNothingConfigured(t *testing.T) {
	withFakeHome(t, t.TempDir())
	stubOAuth(t, func(p string) bool { return p == "openai" })

	got, err := LoadSkillBuilderLLM(t.TempDir(), "", staticEnv(map[string]string{}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Source != SourceGlobalOAuth {
		t.Errorf("Source = %q, want %q", got.Source, SourceGlobalOAuth)
	}
	if got.Provider != "openai" || !got.UseOAuth {
		t.Errorf("want openai + UseOAuth, got %+v", got)
	}
	if got.Model == "" {
		t.Errorf("Model empty, want a default for the global path")
	}
}

// No ui.yaml + no agent + no OAuth + a global provider key => global-env
// default on the first provider whose key is present.
func TestLoad_GlobalDefaultEnvWhenNothingConfigured(t *testing.T) {
	withFakeHome(t, t.TempDir())
	stubOAuth(t, func(p string) bool { return false })

	got, err := LoadSkillBuilderLLM(t.TempDir(), "", staticEnv(map[string]string{
		"ANTHROPIC_API_KEY": "sk-ant",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Source != SourceGlobalEnv {
		t.Errorf("Source = %q, want %q", got.Source, SourceGlobalEnv)
	}
	if got.Provider != "anthropic" || got.APIKey != "sk-ant" {
		t.Errorf("want anthropic + sk-ant, got %+v", got)
	}
}

// Nothing configured and no global credentials => unset (the UI gates on
// this to prompt the operator to configure a provider).
func TestLoad_UnsetWhenNoConfigAndNoGlobalAuth(t *testing.T) {
	withFakeHome(t, t.TempDir())
	stubOAuth(t, func(p string) bool { return false })

	got, err := LoadSkillBuilderLLM(t.TempDir(), "", staticEnv(map[string]string{}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Source != SourceUnset {
		t.Errorf("Source = %q, want %q", got.Source, SourceUnset)
	}
}

// A gateway configured for the ui.yaml provider makes credentials available
// even with no API key or OAuth token — the enterprise-Anthropic path.
func TestLoad_GatewayProvidesCredentialsForConfiguredProvider(t *testing.T) {
	workspace := t.TempDir()
	withFakeHome(t, t.TempDir())
	stubOAuth(t, func(string) bool { return false })
	stubGateway(t, func(_, provider string) bool { return provider == "anthropic" }, nil)

	writeFile(t, filepath.Join(workspace, ".forge", "ui.yaml"), `
skill_builder:
  provider: anthropic
  model: claude-sonnet-4-20250514
`)

	got, err := LoadSkillBuilderLLM(workspace, "", staticEnv(map[string]string{}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.UseGateway {
		t.Errorf("UseGateway = false, want true (gateway configured for anthropic)")
	}
	if !got.HasCredentials() {
		t.Errorf("HasCredentials = false, want true (gateway supplies the token at request time)")
	}
	if got.Provider != "anthropic" {
		t.Errorf("Provider = %q, want anthropic", got.Provider)
	}
}

// No ui.yaml + a settings model default whose provider has a gateway =>
// global-gateway default (the managed enterprise-Anthropic builder LLM).
func TestLoad_GlobalGatewayDefaultFromSettings(t *testing.T) {
	withFakeHome(t, t.TempDir())
	stubOAuth(t, func(string) bool { return false })
	stubGateway(t, func(_, provider string) bool { return provider == "anthropic" },
		&settings.ModelDefault{Provider: "anthropic", Model: "claude-opus-4"})

	got, err := LoadSkillBuilderLLM(t.TempDir(), "", staticEnv(map[string]string{}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Source != SourceGlobalGateway {
		t.Errorf("Source = %q, want %q", got.Source, SourceGlobalGateway)
	}
	if got.Provider != "anthropic" || got.Model != "claude-opus-4" || !got.UseGateway {
		t.Errorf("want anthropic/claude-opus-4 + UseGateway, got %+v", got)
	}
}

// AvailableSkillBuilderLLMs enumerates the default first, then every other
// provider with global credentials — the source of the build-time switch.
func TestAvailable_ListsDefaultThenAlternatives(t *testing.T) {
	workspace := t.TempDir()
	withFakeHome(t, t.TempDir())
	// openai via global env key; anthropic via gateway; gemini unavailable.
	stubOAuth(t, func(string) bool { return false })
	stubGateway(t, func(_, p string) bool { return p == "anthropic" }, nil)

	// ui.yaml picks openai as the default builder LLM.
	writeFile(t, filepath.Join(workspace, ".forge", "ui.yaml"), `
skill_builder:
  provider: openai
  model: gpt-5.4
`)

	list, err := AvailableSkillBuilderLLMs(workspace, "", staticEnv(map[string]string{"OPENAI_API_KEY": "sk-o"}))
	if err != nil {
		t.Fatalf("Available: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 available (openai default + anthropic gateway), got %d: %+v", len(list), list)
	}
	if list[0].Provider != "openai" {
		t.Errorf("default (element 0) = %q, want openai", list[0].Provider)
	}
	if list[1].Provider != "anthropic" || !list[1].UseGateway {
		t.Errorf("alternative = %+v, want anthropic gateway", list[1])
	}
}

func TestResolveForProvider_SelectsAndRejects(t *testing.T) {
	workspace := t.TempDir()
	withFakeHome(t, t.TempDir())
	stubOAuth(t, func(string) bool { return false })
	stubGateway(t, func(_, p string) bool { return p == "anthropic" }, nil)
	writeFile(t, filepath.Join(workspace, ".forge", "ui.yaml"), `
skill_builder:
  provider: openai
  model: gpt-5.4
`)
	env := staticEnv(map[string]string{"OPENAI_API_KEY": "sk-o"})

	// Empty provider → default (openai).
	def, ok, err := ResolveSkillBuilderLLMForProvider(workspace, "", "", env)
	if err != nil || !ok || def.Provider != "openai" {
		t.Fatalf("default resolve: ok=%v provider=%q err=%v", ok, def.Provider, err)
	}
	// Switch to anthropic → the gateway entry.
	an, ok, _ := ResolveSkillBuilderLLMForProvider(workspace, "", "anthropic", env)
	if !ok || an.Provider != "anthropic" || !an.UseGateway {
		t.Errorf("anthropic resolve = %+v ok=%v, want anthropic gateway", an, ok)
	}
	// gemini is unavailable → rejected.
	if _, ok, _ := ResolveSkillBuilderLLMForProvider(workspace, "", "gemini", env); ok {
		t.Errorf("gemini should be unavailable, got ok=true")
	}
}
