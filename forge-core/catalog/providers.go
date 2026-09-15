package catalog

// providers is the canonical, ordered list of LLM providers. The order is the
// order shown in selection UIs.
var providers = []Provider{
	{
		ID:            "openai",
		Label:         "OpenAI",
		Description:   "GPT 6 Astra, GPT 5.6 Sol, Terra, Luna",
		Icon:          "🔷",
		NeedsAPIKey:   true,
		SupportsOAuth: true,
		SupportsOrgID: true,
		APIKeyEnvVar:  "OPENAI_API_KEY",
		DefaultModel:  "gpt-5.6-terra",
		// Order is display order. APIKeyOnly marks models the Codex backend
		// behind ChatGPT sign-in does not serve; see Model.APIKeyOnly.
		Models: []Model{
			{Label: "GPT 6 Astra", ModelID: "gpt-6-astra"},
			{Label: "GPT 5.6 Sol", ModelID: "gpt-5.6-sol"},
			{Label: "GPT 5.6 Terra", ModelID: "gpt-5.6-terra"},
			{Label: "GPT 5.6 Luna", ModelID: "gpt-5.6-luna"},
			// Retired from Codex ChatGPT sign-in on 2026-08-31 (gpt-5.6-terra
			// and gpt-5.6-luna are the documented replacements). OpenAI states
			// the API and API-key Codex are unaffected, so these stay
			// selectable with a key.
			{Label: "GPT 5.4", ModelID: "gpt-5.4", APIKeyOnly: true},
			{Label: "GPT 5 Mini", ModelID: "gpt-5-mini", APIKeyOnly: true},
			// Nano tiers ship API-only and were never offered in Codex.
			{Label: "GPT 5 Nano", ModelID: "gpt-5-nano", APIKeyOnly: true},
			{Label: "GPT 4.1", ModelID: "gpt-4.1", APIKeyOnly: true},
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
