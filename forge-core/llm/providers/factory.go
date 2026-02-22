package providers

import (
	"fmt"

	"github.com/initializ/forge/forge-core/llm"
)

// NewClient creates an LLM client for the specified provider.
// Supported providers: "openai", "anthropic", "ollama".
func NewClient(provider string, cfg llm.ClientConfig) (llm.Client, error) {
	switch provider {
	case "openai":
		return NewOpenAIClient(cfg), nil
	case "anthropic":
		return NewAnthropicClient(cfg), nil
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
