// Package catalog is the single source of truth for the option lists offered
// when configuring an agent: LLM providers (and their models), messaging
// channels, and authentication modes.
//
// Historically these lists were hardcoded inside the `forge init` TUI. They are
// now centralised here so every front-end — the forge-cli TUI, forge-ui, and
// the hosted console (via an HTTP projection) — renders the same choices and
// cannot drift from one another.
//
// The catalog intentionally holds only plain data (no dependency on the TUI or
// any UI toolkit) so it can be consumed by Go callers directly and serialised
// to JSON for browser front-ends.
package catalog

// Provider is a selectable LLM provider.
type Provider struct {
	ID            string  `json:"id"`            // stable identifier, e.g. "openai"
	Label         string  `json:"label"`         // human label, e.g. "OpenAI"
	Description   string  `json:"description"`   // short blurb shown under the label
	Icon          string  `json:"icon"`          // emoji/icon hint
	NeedsAPIKey   bool    `json:"needsApiKey"`   // prompt for an API key
	SupportsOAuth bool    `json:"supportsOAuth"` // offer browser-based OAuth login
	SupportsOrgID bool    `json:"supportsOrgId"` // offer an optional organization ID
	IsCustom      bool    `json:"isCustom"`      // user supplies base URL + model name
	APIKeyEnvVar  string  `json:"apiKeyEnvVar"`  // env var the key is written to (if any)
	Models        []Model `json:"models"`        // selectable models (empty ⇒ no picker)
	DefaultModel  string  `json:"defaultModel"`  // model used when none is picked
}

// Model is a selectable model for a Provider.
type Model struct {
	Label   string `json:"label"`   // e.g. "GPT 5.4"
	ModelID string `json:"modelId"` // e.g. "gpt-5.4"
}

// Channel is a selectable messaging channel connector.
type Channel struct {
	ID          string            `json:"id"`
	Label       string            `json:"label"`
	Description string            `json:"description"`
	Icon        string            `json:"icon"`
	Credentials []CredentialField `json:"credentials"` // tokens/secrets the channel needs
}

// CredentialField is a single credential prompted for a Channel.
type CredentialField struct {
	EnvVar   string `json:"envVar"`   // env var the value is written to
	Prompt   string `json:"prompt"`   // label shown to the user
	Secret   bool   `json:"secret"`   // mask the input
	Optional bool   `json:"optional"` // may be skipped
}

// AuthMode is a selectable A2A authentication mode.
type AuthMode struct {
	ID          string      `json:"id"`
	Label       string      `json:"label"`
	Description string      `json:"description"`
	Icon        string      `json:"icon"`
	Fields      []AuthField `json:"fields"` // settings collected for this mode
}

// AuthField is a single input collected for an AuthMode. Key is the resulting
// key under the auth provider's `settings` block in forge.yaml.
type AuthField struct {
	Key         string `json:"key"`
	Prompt      string `json:"prompt"`
	Placeholder string `json:"placeholder"`
	Required    bool   `json:"required"`
	Default     string `json:"default,omitempty"`
	// Validation names a rule a front-end should apply: "https_url",
	// "non_empty", "aws_region", "account_list", or "" for none.
	Validation string `json:"validation,omitempty"`
}
