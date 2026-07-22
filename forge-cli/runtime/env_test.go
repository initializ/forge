package runtime

import "testing"

// TestMergeModelConfigEnv covers the deployed-pod case: model-config env keys
// (base URLs, provider/model overrides) arrive via the process environment and
// must reach envVars, while a value already loaded from .env / secrets wins.
func TestMergeModelConfigEnv(t *testing.T) {
	t.Run("fills missing keys from the process env", func(t *testing.T) {
		t.Setenv("ANTHROPIC_BASE_URL", "https://kong.internal/anthropic")
		t.Setenv("FORGE_MODEL_PROVIDER", "anthropic")
		envVars := map[string]string{}
		mergeModelConfigEnv(envVars)
		if got := envVars["ANTHROPIC_BASE_URL"]; got != "https://kong.internal/anthropic" {
			t.Errorf("ANTHROPIC_BASE_URL = %q, want it filled from os env", got)
		}
		if got := envVars["FORGE_MODEL_PROVIDER"]; got != "anthropic" {
			t.Errorf("FORGE_MODEL_PROVIDER = %q, want it filled from os env", got)
		}
	})

	t.Run(".env value wins over the process env", func(t *testing.T) {
		t.Setenv("ANTHROPIC_BASE_URL", "https://from-os")
		envVars := map[string]string{"ANTHROPIC_BASE_URL": "https://from-dotenv"}
		mergeModelConfigEnv(envVars)
		if got := envVars["ANTHROPIC_BASE_URL"]; got != "https://from-dotenv" {
			t.Errorf("ANTHROPIC_BASE_URL = %q, want the .env value to win", got)
		}
	})

	t.Run("secret keys are not merged (that's the overlay's job)", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "sk-should-not-flow-here")
		envVars := map[string]string{}
		mergeModelConfigEnv(envVars)
		if _, ok := envVars["ANTHROPIC_API_KEY"]; ok {
			t.Error("mergeModelConfigEnv must not pull secret keys; those go through overlaySecrets")
		}
	})
}
