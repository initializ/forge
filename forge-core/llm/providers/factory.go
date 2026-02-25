package providers

import (
	"fmt"

	"github.com/initializ/forge/forge-core/brain"
	"github.com/initializ/forge/forge-core/llm"
)

// NewClient creates an LLM client for the specified provider.
// Supported providers: "openai", "anthropic", "ollama", "brain".
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
	case "brain":
		return NewBrainClient(cfg)
	default:
		return nil, fmt.Errorf("unknown LLM provider: %q", provider)
	}
}

// NewBrainClient creates a brain-backed LLM client with optional remote fallback.
func NewBrainClient(cfg llm.ClientConfig) (llm.Client, error) {
	modelPath := cfg.ModelPath
	if modelPath == "" {
		// Use default model path
		defaultModel := brain.DefaultModel()
		modelPath = brain.ModelPath(defaultModel.Filename)
	}

	brainCfg := brain.DefaultConfig()
	brainCfg.ModelPath = modelPath

	brainClient, err := brain.NewClient(brainCfg)
	if err != nil {
		return nil, fmt.Errorf("brain provider: %w", err)
	}

	// If an API key is configured, set up a remote fallback
	var remote llm.Client
	if cfg.APIKey != "" && cfg.BaseURL != "" {
		remote = NewOpenAIClient(llm.ClientConfig{
			APIKey:  cfg.APIKey,
			BaseURL: cfg.BaseURL,
			Model:   cfg.Model,
		})
	}

	return brain.NewRouterFromClient(brainClient, remote, brain.DefaultThreshold), nil
}
