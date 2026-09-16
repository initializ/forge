package providers

import (
	"fmt"

	"github.com/initializ/forge/forge-core/llm"
)

// NewClient creates an LLM client for the specified provider.
// Supported providers: "openai", "openai-responses", "anthropic", "bedrock",
// "gemini", "ollama".
func NewClient(provider string, cfg llm.ClientConfig) (llm.Client, error) {
	switch provider {
	case "openai":
		return NewOpenAIClient(cfg), nil
	case llm.ProviderOpenAIResponses:
		// OpenAI Responses API (/v1/responses) via plain API-key / gateway
		// auth — the same client the ChatGPT OAuth path uses, minus the
		// OAuth-only store=false forcing (#383). Composes with auth_scheme.
		return NewResponsesClient(cfg), nil
	case "anthropic":
		return NewAnthropicClient(cfg), nil
	case llm.ProviderBedrock:
		// Native AWS Bedrock Converse API. SigV4 signing is intrinsic —
		// no auth_scheme required. Issue #205.
		return NewBedrockClient(cfg), nil
	case "gemini":
		if cfg.BaseURL == "" {
			cfg.BaseURL = "https://generativelanguage.googleapis.com/v1beta/openai"
		}
		return NewOpenAIClient(cfg), nil
	case "ollama":
		return NewOllamaClient(cfg), nil
	default:
		return nil, fmt.Errorf("unknown LLM provider: %q", provider)
	}
}
