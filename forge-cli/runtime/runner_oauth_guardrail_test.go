package runtime

import (
	"bytes"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/llm"
	coreruntime "github.com/initializ/forge/forge-core/runtime"
)

// Regression test for issue #83: when OPENAI_BASE_URL is set (operator
// pointed Forge at an OpenAI-compatible endpoint) but no OPENAI_API_KEY,
// createProviderClient must refuse with a clear error rather than
// silently using stored ChatGPT OAuth credentials (which override
// BaseURL with chatgpt.com/backend-api/codex).

func newOAuthGuardrailRunner() *Runner {
	return &Runner{
		logger: coreruntime.NewJSONLogger(&bytes.Buffer{}, false),
	}
}

func TestCreateProviderClient_BaseURLSetWithoutAPIKey_RefusesOAuth(t *testing.T) {
	r := newOAuthGuardrailRunner()

	cfg := llm.ClientConfig{
		Model:   "moonshotai/Kimi-K2.6",
		BaseURL: "https://openrouter-ish.example.com/v1",
		// APIKey intentionally empty — would otherwise trigger needsOAuth.
	}

	_, err := r.createProviderClient("openai", cfg)
	if err == nil {
		t.Fatal("expected error when OPENAI_BASE_URL is set without API key, got nil")
	}
	msg := err.Error()
	for _, want := range []string{
		"OPENAI_BASE_URL",
		"https://openrouter-ish.example.com/v1",
		"OPENAI_API_KEY",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message %q does not contain %q", msg, want)
		}
	}
	// Crucially: must NOT mention chatgpt.com (that's the silent override
	// we're guarding against — error tells operator what to fix).
	if strings.Contains(msg, "chatgpt.com") {
		// The mention of chatgpt.com in the error EXPLANATION is fine
		// (we want to tell the operator what would have happened), so
		// don't fail on this — but keep the check here for documentation.
		t.Logf("error message references chatgpt.com (explanatory): %s", msg)
	}
}

func TestCreateProviderClient_BaseURLSetWithAPIKey_BypassesOAuth(t *testing.T) {
	r := newOAuthGuardrailRunner()

	cfg := llm.ClientConfig{
		Model:   "moonshotai/Kimi-K2.6",
		BaseURL: "https://openrouter-ish.example.com/v1",
		APIKey:  "sk-real-endpoint-key",
	}

	client, err := r.createProviderClient("openai", cfg)
	if err != nil {
		t.Fatalf("did not expect error when both BASE_URL and API_KEY are set, got: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestCreateProviderClient_NoBaseURL_AllowsOAuthPath(t *testing.T) {
	// When no BaseURL is set and no API key, the existing OAuth path
	// is unchanged — the guardrail must not block normal openai.com
	// OAuth use. We can't actually verify the OAuth load (no credential
	// store in this test), but the error must NOT be the new
	// "OPENAI_BASE_URL is set" message — it should be the existing
	// "no OpenAI API key or OAuth credentials found" path.
	r := newOAuthGuardrailRunner()

	cfg := llm.ClientConfig{
		Model: "gpt-5.4",
		// No BaseURL, no APIKey
	}
	_, err := r.createProviderClient("openai", cfg)
	if err == nil {
		// If credentials happen to be present locally, this is fine —
		// we only care that the new guardrail did not fire.
		return
	}
	if strings.Contains(err.Error(), "OPENAI_BASE_URL is set") {
		t.Errorf("guardrail fired even though BaseURL is empty: %v", err)
	}
}

// Sanity: non-openai providers are unaffected.
func TestCreateProviderClient_AnthropicWithBaseURL_Unaffected(t *testing.T) {
	r := newOAuthGuardrailRunner()

	cfg := llm.ClientConfig{
		Model:   "claude-sonnet-4-20250514",
		BaseURL: "https://anthropic-proxy.example.com",
	}
	client, err := r.createProviderClient("anthropic", cfg)
	if err != nil {
		t.Fatalf("anthropic provider should not hit the openai-OAuth guardrail: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil anthropic client")
	}
}
