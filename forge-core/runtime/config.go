package runtime

import (
	"github.com/initializ/forge/forge-core/llm"
	"github.com/initializ/forge/forge-core/types"
)

// ModelConfig holds the resolved model provider and configuration.
type ModelConfig struct {
	Provider string
	Client   llm.ClientConfig
}

// ResolveModelConfig resolves the LLM provider and configuration from multiple
// sources with the following priority (highest wins):
//
//  1. CLI --provider flag (providerOverride)
//  2. Environment variables: FORGE_MODEL_PROVIDER, OPENAI_API_KEY, ANTHROPIC_API_KEY, LLM_API_KEY
//  3. forge.yaml model section
//
// Returns nil if no provider could be resolved.
func ResolveModelConfig(cfg *types.ForgeConfig, envVars map[string]string, providerOverride string) *ModelConfig {
	mc := &ModelConfig{}

	// Start with forge.yaml model config
	if cfg.Model.Provider != "" {
		mc.Provider = cfg.Model.Provider
		mc.Client.Model = cfg.Model.Name
	}

	// Apply env vars
	if p := envVars["FORGE_MODEL_PROVIDER"]; p != "" {
		mc.Provider = p
	}
	if m := envVars["MODEL_NAME"]; m != "" {
		mc.Client.Model = m
	}

	// Resolve API key based on provider
	resolveAPIKey(mc, envVars)

	// CLI override is highest priority
	if providerOverride != "" {
		mc.Provider = providerOverride
		resolveAPIKey(mc, envVars)
	}

	// Auto-detect provider from available API keys if not set
	if mc.Provider == "" {
		if envVars["OPENAI_API_KEY"] != "" {
			mc.Provider = "openai"
			mc.Client.APIKey = envVars["OPENAI_API_KEY"]
		} else if envVars["ANTHROPIC_API_KEY"] != "" {
			mc.Provider = "anthropic"
			mc.Client.APIKey = envVars["ANTHROPIC_API_KEY"]
		} else if envVars["GEMINI_API_KEY"] != "" {
			mc.Provider = "gemini"
			mc.Client.APIKey = envVars["GEMINI_API_KEY"]
		}
	}

	// Apply base URL overrides
	if u := envVars["OPENAI_BASE_URL"]; u != "" && mc.Provider == "openai" {
		mc.Client.BaseURL = u
	}
	if u := envVars["ANTHROPIC_BASE_URL"]; u != "" && mc.Provider == "anthropic" {
		mc.Client.BaseURL = u
	}
	if u := envVars["OLLAMA_BASE_URL"]; u != "" && mc.Provider == "ollama" {
		mc.Client.BaseURL = u
	}

	// Return nil if no provider could be resolved
	if mc.Provider == "" {
		return nil
	}

	// Set default models per provider if not specified
	if mc.Client.Model == "" {
		switch mc.Provider {
		case "openai":
			mc.Client.Model = "gpt-4o"
		case "anthropic":
			mc.Client.Model = "claude-sonnet-4-20250514"
		case "gemini":
			mc.Client.Model = "gemini-2.5-flash"
		case "ollama":
			mc.Client.Model = "llama3"
		}
	}

	return mc
}

func resolveAPIKey(mc *ModelConfig, envVars map[string]string) {
	switch mc.Provider {
	case "openai":
		if k := envVars["OPENAI_API_KEY"]; k != "" {
			mc.Client.APIKey = k
		} else if k := envVars["LLM_API_KEY"]; k != "" {
			mc.Client.APIKey = k
		}
	case "anthropic":
		if k := envVars["ANTHROPIC_API_KEY"]; k != "" {
			mc.Client.APIKey = k
		} else if k := envVars["LLM_API_KEY"]; k != "" {
			mc.Client.APIKey = k
		}
	case "gemini":
		if k := envVars["GEMINI_API_KEY"]; k != "" {
			mc.Client.APIKey = k
		} else if k := envVars["LLM_API_KEY"]; k != "" {
			mc.Client.APIKey = k
		}
	case "ollama":
		// Ollama doesn't need an API key
		mc.Client.APIKey = "ollama"
	}
}
