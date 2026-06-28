package security_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/initializ/forge/forge-core/security"
	"github.com/initializ/forge/forge-core/types"
)

// ─── LLMProviderDomains (cfg-side) ───────────────────────────────────

func TestLLMProviderDomains_NilCfg(t *testing.T) {
	if got := security.LLMProviderDomains(nil); got != nil {
		t.Errorf("nil cfg → nil; got %v", got)
	}
}

func TestLLMProviderDomains_NoBaseURL(t *testing.T) {
	cfg := &types.ForgeConfig{
		Model: types.ModelRef{Provider: "openai", Name: "gpt-4o"},
	}
	if got := security.LLMProviderDomains(cfg); got != nil {
		t.Errorf("model with no base_url must contribute no allowlist entry; got %v", got)
	}
}

// TestLLMProviderDomains_PrimaryBaseURL is the load-bearing case for
// issue #139: an OpenAI-compatible provider configured via base_url
// must land in the egress allowlist by hostname (port stripped).
func TestLLMProviderDomains_PrimaryBaseURL(t *testing.T) {
	cfg := &types.ForgeConfig{
		Model: types.ModelRef{
			Provider: "openai",
			Name:     "moonshotai/Kimi-K2.6",
			BaseURL:  "https://api.together.ai/v1",
		},
	}
	got := security.LLMProviderDomains(cfg)
	want := []string{"api.together.ai"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LLMProviderDomains = %v; want %v", got, want)
	}
}

func TestLLMProviderDomains_IncludesFallbacks(t *testing.T) {
	cfg := &types.ForgeConfig{
		Model: types.ModelRef{
			Provider: "openai",
			Name:     "moonshotai/Kimi-K2.6",
			BaseURL:  "https://api.together.ai/v1",
			Fallbacks: []types.ModelFallback{
				{Provider: "anthropic", Name: "claude-sonnet-4", BaseURL: "https://anthropic-proxy.internal/v1"},
				{Provider: "openai", Name: "gpt-4o", BaseURL: "https://openrouter.ai/api/v1"},
			},
		},
	}
	got := security.LLMProviderDomains(cfg)
	sort.Strings(got)
	want := []string{"anthropic-proxy.internal", "api.together.ai", "openrouter.ai"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LLMProviderDomains = %v; want %v", got, want)
	}
}

// TestLLMProviderDomains_PortStripped pins the cross-package contract
// the matcher relies on: allowlist entries are hostnames only, no
// ports. See auth_domains.go hostFromURL comment for the full rationale.
func TestLLMProviderDomains_PortStripped(t *testing.T) {
	cfg := &types.ForgeConfig{
		Model: types.ModelRef{BaseURL: "https://vllm.svc.cluster.local:8000/v1"},
	}
	got := security.LLMProviderDomains(cfg)
	want := []string{"vllm.svc.cluster.local"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("port not stripped; got %v want %v", got, want)
	}
}

func TestLLMProviderDomains_DedupesPrimaryAndFallback(t *testing.T) {
	cfg := &types.ForgeConfig{
		Model: types.ModelRef{
			BaseURL: "https://api.together.ai/v1",
			Fallbacks: []types.ModelFallback{
				// Same host as primary — should not double up.
				{BaseURL: "https://api.together.ai/v1"},
			},
		},
	}
	got := security.LLMProviderDomains(cfg)
	want := []string{"api.together.ai"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("duplicate not deduped; got %v want %v", got, want)
	}
}

func TestLLMProviderDomains_MalformedURL_NoEntry(t *testing.T) {
	cfg := &types.ForgeConfig{
		Model: types.ModelRef{BaseURL: "::::not-a-url::::"},
	}
	if got := security.LLMProviderDomains(cfg); got != nil {
		t.Errorf("malformed URL must produce no entry; got %v", got)
	}
}

// ─── LLMProviderEnvDomains (runtime safety-net) ─────────────────────

func TestLLMProviderEnvDomains_EmptyMap(t *testing.T) {
	if got := security.LLMProviderEnvDomains(nil); got != nil {
		t.Errorf("nil envVars → nil; got %v", got)
	}
	if got := security.LLMProviderEnvDomains(map[string]string{}); got != nil {
		t.Errorf("empty envVars → nil; got %v", got)
	}
}

// TestLLMProviderEnvDomains_AllFourVarsExtracted confirms the runtime
// safety-net picks up every standard SDK base-URL env var even when
// the operator hasn't yet migrated to ModelRef.BaseURL. Order is the
// extraction order (OPENAI / ANTHROPIC / OLLAMA / GEMINI).
func TestLLMProviderEnvDomains_AllFourVarsExtracted(t *testing.T) {
	envVars := map[string]string{
		"OPENAI_BASE_URL":    "https://api.together.ai/v1",
		"ANTHROPIC_BASE_URL": "https://anthropic-proxy.internal/v1",
		"OLLAMA_BASE_URL":    "http://ollama.svc.cluster.local:11434",
		"GEMINI_BASE_URL":    "https://gemini-proxy.internal/v1beta",
		"UNRELATED":          "https://nope.example.com", // must NOT be picked up
	}
	got := security.LLMProviderEnvDomains(envVars)
	want := []string{
		"api.together.ai",
		"anthropic-proxy.internal",
		"ollama.svc.cluster.local",
		"gemini-proxy.internal",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LLMProviderEnvDomains = %v; want %v", got, want)
	}
}

func TestLLMProviderEnvDomains_OnlyOneSet(t *testing.T) {
	envVars := map[string]string{
		"OPENAI_BASE_URL": "https://api.together.ai/v1",
	}
	got := security.LLMProviderEnvDomains(envVars)
	want := []string{"api.together.ai"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("single-var case: got %v want %v", got, want)
	}
}

func TestLLMProviderEnvDomains_EmptyValueSkipped(t *testing.T) {
	envVars := map[string]string{
		"OPENAI_BASE_URL":    "",
		"ANTHROPIC_BASE_URL": "https://anthropic-proxy.internal",
	}
	got := security.LLMProviderEnvDomains(envVars)
	want := []string{"anthropic-proxy.internal"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("empty value must skip; got %v want %v", got, want)
	}
}

func TestLLMProviderEnvDomains_DedupesSameHost(t *testing.T) {
	envVars := map[string]string{
		"OPENAI_BASE_URL":    "https://api.together.ai/v1",
		"ANTHROPIC_BASE_URL": "https://api.together.ai/anthropic-shim", // same host
	}
	got := security.LLMProviderEnvDomains(envVars)
	want := []string{"api.together.ai"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("same-host dedup: got %v want %v", got, want)
	}
}

func TestLLMProviderEnvDomains_MalformedURL_NoEntry(t *testing.T) {
	envVars := map[string]string{
		"OPENAI_BASE_URL": "::::not-a-url::::",
	}
	if got := security.LLMProviderEnvDomains(envVars); got != nil {
		t.Errorf("malformed env URL must produce no entry; got %v", got)
	}
}

// TestLLMProviderDomains_BedrockHostExtracted pins the issue #202
// Phase 2 invariant: when an operator points the Anthropic or OpenAI
// provider at AWS Bedrock (via `model.base_url`), the Bedrock
// hostname auto-extends the egress allowlist — they don't need to
// also remember to write `bedrock-runtime.<region>.amazonaws.com`
// under `egress.allowed_domains`. Existing hostFromURL is generic; this
// test is a regression pin so a future Bedrock URL-shape change
// (regional differences, e.g. govcloud) still flows through cleanly.
func TestLLMProviderDomains_BedrockHostExtracted(t *testing.T) {
	cfg := &types.ForgeConfig{
		Model: types.ModelRef{
			Provider:   "anthropic",
			Name:       "anthropic.claude-sonnet-4-20250514-v1:0",
			BaseURL:    "https://bedrock-runtime.us-east-1.amazonaws.com",
			AuthScheme: "aws_sigv4",
			AWSRegion:  "us-east-1",
		},
	}
	got := security.LLMProviderDomains(cfg)
	want := []string{"bedrock-runtime.us-east-1.amazonaws.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Bedrock hostname not in allowlist; got %v want %v", got, want)
	}
}
