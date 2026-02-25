package runtime

import (
	"strings"

	"github.com/initializ/forge/forge-core/brain"
	"github.com/initializ/forge/forge-core/llm"
	"github.com/initializ/forge/forge-core/types"
)

// ModelConfig holds the resolved model provider and configuration.
type ModelConfig struct {
	Provider  string
	Client    llm.ClientConfig
	Fallbacks []FallbackModelConfig
}

// FallbackModelConfig holds the resolved configuration for a fallback provider.
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
		} else if brain.IsModelDownloaded(brain.DefaultModel().Filename) {
			// Fall back to brain if a local model is available
			mc.Provider = "brain"
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

	// Resolve brain model path from env
	if mc.Provider == "brain" {
		if p := envVars["FORGE_BRAIN_MODEL"]; p != "" {
			mc.Client.ModelPath = p
		}
	}

	// Set default models per provider if not specified
	if mc.Client.Model == "" {
		setDefaultModel(mc.Provider, &mc.Client)
	}

	// Resolve fallback providers
	mc.Fallbacks = resolveFallbacks(cfg, envVars, mc.Provider)

	return mc
}

// resolveFallbacks resolves fallback providers from three sources:
//  1. forge.yaml model.fallbacks list
//  2. FORGE_MODEL_FALLBACKS env var (format: "provider:model,provider:model")
//  3. Auto-detect: unused API keys not matching the primary provider
func resolveFallbacks(cfg *types.ForgeConfig, envVars map[string]string, primary string) []FallbackModelConfig {
	var fallbacks []FallbackModelConfig

	// Source 1: forge.yaml model.fallbacks
	for _, fb := range cfg.Model.Fallbacks {
		fmc := FallbackModelConfig{Provider: fb.Provider}
		fmc.Client.Model = fb.Name
		resolveFallbackAPIKey(&fmc, envVars)
		setDefaultModel(fmc.Provider, &fmc.Client)
		applyBaseURL(fmc.Provider, &fmc.Client, envVars)
		if fmc.Client.APIKey != "" {
			fallbacks = append(fallbacks, fmc)
		}
	}

	if len(fallbacks) > 0 {
		return fallbacks
	}

	// Source 2: FORGE_MODEL_FALLBACKS env var
	if envFallbacks := envVars["FORGE_MODEL_FALLBACKS"]; envFallbacks != "" {
		for _, entry := range strings.Split(envFallbacks, ",") {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			provider, model, _ := strings.Cut(entry, ":")
			provider = strings.TrimSpace(provider)
			model = strings.TrimSpace(model)
			if provider == "" || provider == primary {
				continue
			}
			fmc := FallbackModelConfig{Provider: provider}
			fmc.Client.Model = model
			resolveFallbackAPIKey(&fmc, envVars)
			setDefaultModel(fmc.Provider, &fmc.Client)
			applyBaseURL(fmc.Provider, &fmc.Client, envVars)
			if fmc.Client.APIKey != "" {
				fallbacks = append(fallbacks, fmc)
			}
		}
		if len(fallbacks) > 0 {
			return fallbacks
		}
	}

	// Source 3: Auto-detect from available API keys (exclude brain/ollama as local)
	autoProviders := []struct {
		provider string
		keyEnv   string
	}{
		{"openai", "OPENAI_API_KEY"},
		{"anthropic", "ANTHROPIC_API_KEY"},
		{"gemini", "GEMINI_API_KEY"},
	}

	for _, ap := range autoProviders {
		if ap.provider == primary {
			continue
		}
		if key := envVars[ap.keyEnv]; key != "" {
			fmc := FallbackModelConfig{Provider: ap.provider}
			fmc.Client.APIKey = key
			setDefaultModel(fmc.Provider, &fmc.Client)
			applyBaseURL(fmc.Provider, &fmc.Client, envVars)
			fallbacks = append(fallbacks, fmc)
		}
	}

	return fallbacks
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
	case "brain":
		// Brain doesn't need an API key
		mc.Client.APIKey = "brain"
	}
}

// resolveFallbackAPIKey resolves the API key for a fallback provider.
func resolveFallbackAPIKey(fmc *FallbackModelConfig, envVars map[string]string) {
	switch fmc.Provider {
	case "openai":
		if k := envVars["OPENAI_API_KEY"]; k != "" {
			fmc.Client.APIKey = k
		} else if k := envVars["LLM_API_KEY"]; k != "" {
			fmc.Client.APIKey = k
		}
	case "anthropic":
		if k := envVars["ANTHROPIC_API_KEY"]; k != "" {
			fmc.Client.APIKey = k
		} else if k := envVars["LLM_API_KEY"]; k != "" {
			fmc.Client.APIKey = k
		}
	case "gemini":
		if k := envVars["GEMINI_API_KEY"]; k != "" {
			fmc.Client.APIKey = k
		} else if k := envVars["LLM_API_KEY"]; k != "" {
			fmc.Client.APIKey = k
		}
	case "ollama":
		fmc.Client.APIKey = "ollama"
	case "brain":
		fmc.Client.APIKey = "brain"
	}
}

// setDefaultModel sets the default model for a provider if not specified.
func setDefaultModel(provider string, cfg *llm.ClientConfig) {
	if cfg.Model != "" {
		return
	}
	switch provider {
	case "openai":
		cfg.Model = "gpt-4o"
	case "anthropic":
		cfg.Model = "claude-sonnet-4-20250514"
	case "gemini":
		cfg.Model = "gemini-2.5-flash"
	case "ollama":
		cfg.Model = "llama3"
	case "brain":
		cfg.Model = brain.DefaultModel().ID
	}
}

// applyBaseURL applies provider-specific base URL overrides from env vars.
func applyBaseURL(provider string, cfg *llm.ClientConfig, envVars map[string]string) {
	switch provider {
	case "openai":
		if u := envVars["OPENAI_BASE_URL"]; u != "" {
			cfg.BaseURL = u
		}
	case "anthropic":
		if u := envVars["ANTHROPIC_BASE_URL"]; u != "" {
			cfg.BaseURL = u
		}
	case "ollama":
		if u := envVars["OLLAMA_BASE_URL"]; u != "" {
			cfg.BaseURL = u
		}
	}
}
