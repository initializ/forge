package runtime

import (
	"strings"

	"github.com/initializ/forge/forge-core/llm"
	"github.com/initializ/forge/forge-core/types"
)

// ModelConfig holds the resolved model provider and configuration.
type ModelConfig struct {
	Provider  string
	Client    llm.ClientConfig
	Fallbacks []FallbackModelConfig
}

// FallbackModelConfig holds a resolved fallback provider's configuration.
type FallbackModelConfig struct {
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
		mc.Client.Model = defaultModelForProvider(mc.Provider)
	}

	// Resolve fallback providers
	mc.Fallbacks = resolveFallbacks(cfg, envVars, mc.Provider)

	return mc
}

// defaultModelForProvider returns the default model name for a given provider.
func defaultModelForProvider(provider string) string {
	switch provider {
	case "openai":
		return "gpt-5.2-2025-12-11"
	case "anthropic":
		return "claude-sonnet-4-20250514"
	case "gemini":
		return "gemini-2.5-flash"
	case "ollama":
		return "llama3"
	default:
		return ""
	}
}

// resolveFallbacks resolves fallback provider configurations from multiple sources:
// 1. forge.yaml model.fallbacks
// 2. FORGE_MODEL_FALLBACKS env var (format: "openai:gpt-4o,gemini:gemini-2.5-flash")
// 3. Auto-detection from available API keys
func resolveFallbacks(cfg *types.ForgeConfig, envVars map[string]string, primaryProvider string) []FallbackModelConfig {
	seen := map[string]bool{primaryProvider: true}
	var fallbacks []FallbackModelConfig

	addFallback := func(provider, model string) {
		if seen[provider] {
			return
		}
		apiKey := resolveFallbackAPIKey(provider, envVars)
		if apiKey == "" && provider != "ollama" {
			return // skip providers without API keys
		}
		seen[provider] = true
		if model == "" {
			model = defaultModelForProvider(provider)
		}
		fc := FallbackModelConfig{
			Provider: provider,
			Client: llm.ClientConfig{
				APIKey: apiKey,
				Model:  model,
			},
		}
		if provider == "ollama" && apiKey == "" {
			fc.Client.APIKey = "ollama"
		}
		// Apply base URL overrides
		fc.Client.BaseURL = resolveFallbackBaseURL(provider, envVars)
		fallbacks = append(fallbacks, fc)
	}

	// Source 1: forge.yaml model.fallbacks
	for _, fb := range cfg.Model.Fallbacks {
		addFallback(fb.Provider, fb.Name)
	}

	// Source 2: FORGE_MODEL_FALLBACKS env var
	if envFallbacks := envVars["FORGE_MODEL_FALLBACKS"]; envFallbacks != "" {
		for _, entry := range strings.Split(envFallbacks, ",") {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			provider, model, _ := strings.Cut(entry, ":")
			addFallback(provider, model)
		}
	}

	// Source 3: Auto-detect from available API keys
	providerKeys := map[string]string{
		"openai":    "OPENAI_API_KEY",
		"anthropic": "ANTHROPIC_API_KEY",
		"gemini":    "GEMINI_API_KEY",
	}
	for provider, keyName := range providerKeys {
		if envVars[keyName] != "" {
			addFallback(provider, "")
		}
	}

	return fallbacks
}

// resolveFallbackAPIKey resolves the API key for a fallback provider.
func resolveFallbackAPIKey(provider string, envVars map[string]string) string {
	switch provider {
	case "openai":
		return envVars["OPENAI_API_KEY"]
	case "anthropic":
		return envVars["ANTHROPIC_API_KEY"]
	case "gemini":
		return envVars["GEMINI_API_KEY"]
	case "ollama":
		return "ollama"
	default:
		return envVars["LLM_API_KEY"]
	}
}

// resolveFallbackBaseURL resolves the base URL for a fallback provider.
func resolveFallbackBaseURL(provider string, envVars map[string]string) string {
	switch provider {
	case "openai":
		return envVars["OPENAI_BASE_URL"]
	case "anthropic":
		return envVars["ANTHROPIC_BASE_URL"]
	case "ollama":
		return envVars["OLLAMA_BASE_URL"]
	default:
		return ""
	}
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
