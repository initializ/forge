package catalog

// providers is the canonical, ordered list of LLM providers. The order is the
// order shown in selection UIs.
var providers = []Provider{
	{
		ID:            "openai",
		Label:         "OpenAI",
		Description:   "GPT 5.4, GPT 5 Mini, GPT 5 Nano",
		Icon:          "🔷",
		NeedsAPIKey:   true,
		SupportsOAuth: true,
		SupportsOrgID: true,
		APIKeyEnvVar:  "OPENAI_API_KEY",
		DefaultModel:  "gpt-5.4",
		Models: []Model{
			{Label: "GPT 5.4", ModelID: "gpt-5.4"},
			{Label: "GPT 5 Mini", ModelID: "gpt-5-mini"},
			{Label: "GPT 5 Nano", ModelID: "gpt-5-nano"},
			{Label: "GPT 4.1", ModelID: "gpt-4.1"},
		},
	},
	{
		ID:           "anthropic",
		Label:        "Anthropic",
		Description:  "Claude Sonnet, Haiku, Opus",
		Icon:         "🟠",
		NeedsAPIKey:  true,
		APIKeyEnvVar: "ANTHROPIC_API_KEY",
		DefaultModel: "claude-sonnet-4-20250514",
	},
	{
		ID:           "gemini",
		Label:        "Google Gemini",
		Description:  "Gemini 2.5 Flash, Pro",
		Icon:         "🔵",
		NeedsAPIKey:  true,
		APIKeyEnvVar: "GEMINI_API_KEY",
		DefaultModel: "gemini-2.5-flash",
	},
	{
		ID:           "ollama",
		Label:        "Ollama (local)",
		Description:  "Run models locally, no API key needed",
		Icon:         "🦙",
		DefaultModel: "llama3",
	},
	{
		ID:          "custom",
		Label:       "Custom URL",
		Description: "OpenAI-compatible or Anthropic-compatible endpoint",
		Icon:        "⚙️",
		IsCustom:    true,
	},
}

// AllProviders returns the catalog of LLM providers in display order.
func AllProviders() []Provider { return providers }

// ProviderByID returns the provider with the given id, and whether it was found.
func ProviderByID(id string) (Provider, bool) {
	for _, p := range providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}
