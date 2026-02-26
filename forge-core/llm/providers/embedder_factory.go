package providers

import (
	"fmt"

	"github.com/initializ/forge/forge-core/llm"
)

// NewEmbedder creates an Embedder for the specified provider.
// Supported providers: "openai", "gemini", "ollama".
// Returns an error for "anthropic" (no embedding API).
func NewEmbedder(provider string, cfg OpenAIEmbedderConfig) (llm.Embedder, error) {
	switch provider {
	case "openai":
		return NewOpenAIEmbedder(cfg), nil
	case "gemini":
		if cfg.BaseURL == "" {
			cfg.BaseURL = "https://generativelanguage.googleapis.com/v1beta/openai"
		}
		return NewOpenAIEmbedder(cfg), nil
	case "ollama":
		return NewOllamaEmbedder(cfg), nil
	case "anthropic":
		return nil, fmt.Errorf("anthropic does not provide an embedding API; configure an alternative embedding provider")
	default:
		return nil, fmt.Errorf("unknown embedding provider: %q", provider)
	}
}
