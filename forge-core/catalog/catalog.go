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
	ID            string `json:"id"`            // stable identifier, e.g. "openai"
	Label         string `json:"label"`         // human label, e.g. "OpenAI"
	Description   string `json:"description"`   // short blurb shown under the label
	Icon          string `json:"icon"`          // emoji/icon hint
	NeedsAPIKey   bool   `json:"needsApiKey"`   // prompt for an API key
	SupportsOAuth bool   `json:"supportsOAuth"` // offer browser-based OAuth login
	SupportsOrgID bool   `json:"supportsOrgId"` // offer an optional organization ID
	IsCustom      bool   `json:"isCustom"`      // user supplies base URL + model name
	// NeedsAWSRegion prompts for an AWS region (written to model.aws_region)
	// instead of an API key. Set for provider "bedrock", whose native
	// Converse client signs with SigV4 from AWS env credentials and needs
	// the region for both the endpoint host and the signature scope (#205).
	NeedsAWSRegion bool    `json:"needsAwsRegion"`
	APIKeyEnvVar   string  `json:"apiKeyEnvVar"` // env var the key is written to (if any)
	Models         []Model `json:"models"`       // selectable models (empty ⇒ no picker)
	DefaultModel   string  `json:"defaultModel"` // model used when none is picked
}

// Model is a selectable model for a Provider.
type Model struct {
	Label   string `json:"label"`   // e.g. "GPT 5.6 Terra"
	ModelID string `json:"modelId"` // e.g. "gpt-5.6-terra"
	// APIKeyOnly marks a model that cannot be reached through browser-based
	// OAuth login. OpenAI's ChatGPT sign-in tokens are scoped to the Codex
	// backend, which serves a narrower set than the plain API — so a model
	// retired from Codex is still perfectly usable with an API key. Offering
	// one on the OAuth path produces an agent that fails on first call.
	APIKeyOnly bool `json:"apiKeyOnly,omitempty"`
}

// OAuthModels returns the models selectable when the user authenticates via
// browser-based OAuth login, i.e. everything not marked APIKeyOnly.
func (p Provider) OAuthModels() []Model {
	out := make([]Model, 0, len(p.Models))
	for _, m := range p.Models {
		if !m.APIKeyOnly {
			out = append(out, m)
		}
	}
	return out
}

// APIKeyModels returns the models selectable when the user supplies an API
// key — every model the provider offers.
func (p Provider) APIKeyModels() []Model { return p.Models }

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
