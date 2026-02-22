package providers

import "github.com/initializ/forge/forge-core/llm"

// OllamaClient wraps OpenAIClient with Ollama-specific defaults.
// Ollama provides an OpenAI-compatible API at localhost:11434/v1.
type OllamaClient struct {
	*OpenAIClient
}

// NewOllamaClient creates a client that talks to a local Ollama server.
func NewOllamaClient(cfg llm.ClientConfig) *OllamaClient {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://localhost:11434/v1"
	}
	if cfg.APIKey == "" {
		cfg.APIKey = "ollama" // Ollama requires a non-empty key
	}
	return &OllamaClient{
		OpenAIClient: NewOpenAIClient(cfg),
	}
}
