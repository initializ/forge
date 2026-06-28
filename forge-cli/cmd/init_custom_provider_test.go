package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Regression tests for issue #83 — Custom-provider wizard path produces
// forge.yaml + .env that the runtime can actually consume.

func TestNormalizeCustomProvider_RewritesLegacyEnvVars(t *testing.T) {
	opts := &initOptions{
		ModelProvider: "custom",
		EnvVars: map[string]string{
			"MODEL_BASE_URL": "https://endpoint.example.com/v1",
			"MODEL_API_KEY":  "sk-test",
			"UNRELATED":      "keep-me",
		},
	}
	normalizeCustomProvider(opts)

	if opts.ModelProvider != "openai" {
		t.Errorf("ModelProvider = %q, want %q", opts.ModelProvider, "openai")
	}
	if got := opts.EnvVars["OPENAI_BASE_URL"]; got != "https://endpoint.example.com/v1" {
		t.Errorf("OPENAI_BASE_URL = %q, want endpoint URL", got)
	}
	if got := opts.EnvVars["OPENAI_API_KEY"]; got != "sk-test" {
		t.Errorf("OPENAI_API_KEY = %q, want sk-test", got)
	}
	if _, present := opts.EnvVars["MODEL_BASE_URL"]; present {
		t.Errorf("MODEL_BASE_URL should be deleted after normalization")
	}
	if _, present := opts.EnvVars["MODEL_API_KEY"]; present {
		t.Errorf("MODEL_API_KEY should be deleted after normalization")
	}
	if got := opts.EnvVars["UNRELATED"]; got != "keep-me" {
		t.Errorf("UNRELATED key should be preserved, got %q", got)
	}
	if opts.APIKey != "sk-test" {
		t.Errorf("opts.APIKey should be filled from MODEL_API_KEY, got %q", opts.APIKey)
	}
}

func TestNormalizeCustomProvider_NoOpForOtherProviders(t *testing.T) {
	cases := []string{"openai", "anthropic", "gemini", "ollama"}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			opts := &initOptions{
				ModelProvider: p,
				EnvVars: map[string]string{
					"MODEL_BASE_URL": "https://should-not-be-touched",
					"MODEL_API_KEY":  "should-not-be-touched",
				},
			}
			normalizeCustomProvider(opts)
			if opts.ModelProvider != p {
				t.Errorf("ModelProvider mutated from %q to %q", p, opts.ModelProvider)
			}
			if got := opts.EnvVars["MODEL_BASE_URL"]; got != "https://should-not-be-touched" {
				t.Errorf("MODEL_BASE_URL should not be touched for provider=%s, got %q", p, got)
			}
		})
	}
}

func TestNormalizeCustomProvider_PreExistingOpenAIVarsPreserved(t *testing.T) {
	// Newer Web UI revs write OPENAI_BASE_URL directly. The normalizer
	// should accept that and not clobber it.
	opts := &initOptions{
		ModelProvider: "custom",
		EnvVars: map[string]string{
			"OPENAI_BASE_URL": "https://from-webui",
			"OPENAI_API_KEY":  "sk-from-webui",
		},
	}
	normalizeCustomProvider(opts)

	if opts.ModelProvider != "openai" {
		t.Errorf("ModelProvider = %q, want openai", opts.ModelProvider)
	}
	if got := opts.EnvVars["OPENAI_BASE_URL"]; got != "https://from-webui" {
		t.Errorf("OPENAI_BASE_URL clobbered, got %q", got)
	}
	if got := opts.EnvVars["OPENAI_API_KEY"]; got != "sk-from-webui" {
		t.Errorf("OPENAI_API_KEY clobbered, got %q", got)
	}
}

func TestNormalizeCustomProvider_APIKeyFallsBackToOptsField(t *testing.T) {
	// Non-interactive --api-key flag path: APIKey is set on opts directly
	// (storeProviderEnvVar would skip the openai branch when provider was
	// still "custom"). After normalization, the API key must reach OPENAI_API_KEY.
	opts := &initOptions{
		ModelProvider: "custom",
		APIKey:        "sk-from-flag",
		EnvVars:       map[string]string{"MODEL_BASE_URL": "https://endpoint"},
	}
	normalizeCustomProvider(opts)

	if opts.ModelProvider != "openai" {
		t.Errorf("ModelProvider = %q, want openai", opts.ModelProvider)
	}
	if got := opts.EnvVars["OPENAI_API_KEY"]; got != "sk-from-flag" {
		t.Errorf("OPENAI_API_KEY = %q, want sk-from-flag", got)
	}
	if got := opts.EnvVars["OPENAI_BASE_URL"]; got != "https://endpoint" {
		t.Errorf("OPENAI_BASE_URL = %q, want endpoint", got)
	}
}

// End-to-end: scaffold with the Custom provider shape produces a
// forge.yaml whose model.provider is "openai" and a .env containing
// OPENAI_BASE_URL + OPENAI_API_KEY (not MODEL_*).
func TestScaffold_CustomProviderProducesOpenAIShape(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	opts := &initOptions{
		Name:          "custom-shape",
		AgentID:       "custom-shape",
		Framework:     "forge",
		ModelProvider: "custom",
		CustomModel:   "moonshotai/Kimi-K2.6",
		APIKey:        "sk-endpoint",
		EnvVars: map[string]string{
			"MODEL_BASE_URL": "https://openrouter-ish.example.com/v1",
			"MODEL_API_KEY":  "sk-endpoint",
		},
		NonInteractive: true,
	}

	if err := scaffold(opts); err != nil {
		t.Fatalf("scaffold: %v", err)
	}

	// forge.yaml: provider must be "openai" (not "custom"), model.name preserved.
	cfgPath := filepath.Join("custom-shape", "forge.yaml")
	cfgRaw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("reading forge.yaml: %v", err)
	}
	var cfg struct {
		Model struct {
			Provider string `yaml:"provider"`
			Name     string `yaml:"name"`
		} `yaml:"model"`
	}
	if err := yaml.Unmarshal(cfgRaw, &cfg); err != nil {
		t.Fatalf("parsing forge.yaml: %v\n%s", err, cfgRaw)
	}
	if cfg.Model.Provider != "openai" {
		t.Errorf("forge.yaml model.provider = %q, want %q\n--- forge.yaml ---\n%s",
			cfg.Model.Provider, "openai", cfgRaw)
	}
	if cfg.Model.Name != "moonshotai/Kimi-K2.6" {
		t.Errorf("forge.yaml model.name = %q, want %q", cfg.Model.Name, "moonshotai/Kimi-K2.6")
	}

	// .env: OPENAI_BASE_URL + OPENAI_API_KEY present; legacy MODEL_* absent.
	envPath := filepath.Join("custom-shape", ".env")
	envContent, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("reading .env: %v", err)
	}
	envStr := string(envContent)
	if !strings.Contains(envStr, "OPENAI_BASE_URL=https://openrouter-ish.example.com/v1") {
		t.Errorf(".env missing OPENAI_BASE_URL with endpoint URL:\n%s", envStr)
	}
	if !strings.Contains(envStr, "OPENAI_API_KEY=sk-endpoint") {
		t.Errorf(".env missing OPENAI_API_KEY=sk-endpoint:\n%s", envStr)
	}
	if strings.Contains(envStr, "MODEL_BASE_URL=") {
		t.Errorf(".env should NOT contain MODEL_BASE_URL after normalization:\n%s", envStr)
	}
	if strings.Contains(envStr, "MODEL_API_KEY=") {
		t.Errorf(".env should NOT contain MODEL_API_KEY after normalization:\n%s", envStr)
	}
}

// Web UI parity: a POST whose ModelProvider="custom" + EnvVars already
// carry OPENAI_BASE_URL (new app.js shape) also produces the right output.
func TestScaffold_CustomProviderWebUIShape(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	opts := &initOptions{
		Name:          "webui-shape",
		AgentID:       "webui-shape",
		Framework:     "forge",
		ModelProvider: "custom",
		CustomModel:   "moonshotai/Kimi-K2.6",
		EnvVars: map[string]string{
			"OPENAI_BASE_URL": "https://endpoint.example.com/v1",
			"OPENAI_API_KEY":  "sk-from-webui",
		},
		NonInteractive: true,
	}

	if err := scaffold(opts); err != nil {
		t.Fatalf("scaffold: %v", err)
	}

	envPath := filepath.Join("webui-shape", ".env")
	envContent, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("reading .env: %v", err)
	}
	envStr := string(envContent)
	if !strings.Contains(envStr, "OPENAI_BASE_URL=https://endpoint.example.com/v1") {
		t.Errorf(".env missing OPENAI_BASE_URL:\n%s", envStr)
	}
	if !strings.Contains(envStr, "OPENAI_API_KEY=sk-from-webui") {
		t.Errorf(".env missing OPENAI_API_KEY=sk-from-webui:\n%s", envStr)
	}
}

// TestNormalizeCustomProvider_AnthropicShapeRewritesToAnthropic is
// the issue #202 Phase 1 pin: when the wizard's shape picker chose
// "anthropic", the normalizer rewrites MODEL_* env vars onto the
// ANTHROPIC_* names AND flips the provider from "custom" to
// "anthropic". Generated forge.yaml then carries
// `provider: anthropic` and the runtime's ResolveModelConfig wires
// ANTHROPIC_BASE_URL onto the client.
func TestNormalizeCustomProvider_AnthropicShapeRewritesToAnthropic(t *testing.T) {
	opts := &initOptions{
		ModelProvider: "custom",
		EnvVars: map[string]string{
			"__custom_shape": "anthropic",
			"MODEL_BASE_URL": "https://bedrock-runtime.us-east-1.amazonaws.com",
			"MODEL_API_KEY":  "ignored-on-sigv4",
		},
	}
	normalizeCustomProvider(opts)

	if opts.ModelProvider != "anthropic" {
		t.Errorf("ModelProvider = %q, want anthropic", opts.ModelProvider)
	}
	if got := opts.EnvVars["ANTHROPIC_BASE_URL"]; got != "https://bedrock-runtime.us-east-1.amazonaws.com" {
		t.Errorf("ANTHROPIC_BASE_URL = %q, want bedrock URL", got)
	}
	if got := opts.EnvVars["ANTHROPIC_API_KEY"]; got != "ignored-on-sigv4" {
		t.Errorf("ANTHROPIC_API_KEY = %q, want carried-over key", got)
	}
	if _, present := opts.EnvVars["MODEL_BASE_URL"]; present {
		t.Errorf("MODEL_BASE_URL should be deleted")
	}
	if _, present := opts.EnvVars["__custom_shape"]; present {
		t.Errorf("__custom_shape synthetic env key must be stripped before write")
	}
}

// TestNormalizeCustomProvider_DefaultShapeStaysOpenAI confirms back-
// compat: no __custom_shape in env (e.g. Web UI Custom flow that
// hasn't gained the picker yet) keeps the legacy default of OpenAI.
func TestNormalizeCustomProvider_DefaultShapeStaysOpenAI(t *testing.T) {
	opts := &initOptions{
		ModelProvider: "custom",
		EnvVars: map[string]string{
			"MODEL_BASE_URL": "https://openrouter.example/v1",
			"MODEL_API_KEY":  "sk-router",
		},
	}
	normalizeCustomProvider(opts)
	if opts.ModelProvider != "openai" {
		t.Errorf("default-shape should map to openai; got %q", opts.ModelProvider)
	}
	if got := opts.EnvVars["OPENAI_BASE_URL"]; got == "" {
		t.Errorf("OPENAI_BASE_URL should be set on default path")
	}
}
