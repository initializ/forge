package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

// FallbackProvider holds a fallback provider selection from the wizard.
type FallbackProvider struct {
	Provider string
	APIKey   string
}

// WizardContext accumulates all data across wizard steps.
type WizardContext struct {
	Name           string
	Provider       string
	APIKey         string
	AuthMethod     string // "apikey" or "oauth" — how the user authenticated
	ModelName      string // selected model ID (e.g. "gpt-5.4")
	OrganizationID string // OpenAI enterprise organization ID
	AWSRegion      string // AWS region for provider "bedrock" (model.aws_region, #205)
	Fallbacks      []FallbackProvider
	Channel        string
	ChannelTokens  map[string]string
	BuiltinTools   []string
	Skills         []string
	EgressDomains  []string
	CustomBaseURL  string
	CustomModel    string
	CustomAPIKey   string
	// CustomShape selects which wire format the custom-URL endpoint
	// speaks — "openai" (default; OpenAI Chat Completions, used by
	// litellm / vLLM / OpenRouter / self-hosted Llama proxies) or
	// "anthropic" (Anthropic Messages format, used by Bedrock's
	// Anthropic passthrough and by Anthropic-compatible proxies).
	// Drives whether normalizeCustomProvider sets ModelProvider to
	// "openai" or "anthropic" and which BASE_URL / API_KEY env names
	// are emitted to .env. See issue #202 Phase 1.
	CustomShape string
	EnvVars     map[string]string

	// Compression enables reversible context compression (ctxzip): bulky
	// tool outputs are compressed before reaching the LLM and stay
	// retrievable via the context_expand tool. Written to forge.yaml as
	// compression.enabled.
	Compression bool

	// ImageGeneration opts the model into generating image output (#255),
	// written to forge.yaml as model.image_generation. Only offered by the
	// wizard when the selected provider+model supports it
	// (runtime.ModelSupportsImageGen): openai-responses, or a Gemini image
	// model. Honored by the openai-responses + gemini clients.
	ImageGeneration bool

	// AuthMode is the user's selection from the auth step:
	//   ""             — wizard step did not run
	//   "none"         — anonymous access (no auth: block written)
	//   "oidc"         — OIDC provider
	//   "http_verifier" — legacy external verifier
	//   "custom"       — write a commented stub; user edits manually
	AuthMode string

	// AuthSettings is the type-specific settings block for the chosen
	// provider, ready to plug into `auth.providers[0].settings` in
	// forge.yaml. Keys: "issuer", "audience", "url", "default_org",
	// "claim_map" (a nested map), etc.
	AuthSettings map[string]any

	// AuthEgressHosts is the list of bare hosts (no port) that should be
	// auto-added to the egress allowlist because the auth step requires
	// them — e.g. an OIDC issuer or http_verifier URL. The init scaffold
	// merges these into the rendered forge.yaml egress.allowed_domains.
	AuthEgressHosts []string
}

// NewWizardContext creates an initialized WizardContext.
func NewWizardContext() *WizardContext {
	return &WizardContext{
		ChannelTokens: make(map[string]string),
		EnvVars:       make(map[string]string),
		AuthSettings:  make(map[string]any),
	}
}

// WizardModel is the top-level bubbletea model that orchestrates the wizard.
type WizardModel struct {
	styles  *StyleSet
	theme   TermTheme
	steps   []Step
	current int
	ctx     *WizardContext
	width   int
	height  int
	done    bool
	err     error
	version string
}

// NewWizardModel creates a new wizard with the given steps.
func NewWizardModel(theme TermTheme, steps []Step, version string) WizardModel {
	return WizardModel{
		styles:  NewStyleSet(theme),
		theme:   theme,
		steps:   steps,
		ctx:     NewWizardContext(),
		width:   80,
		height:  24,
		version: version,
	}
}

// Init initializes the first step.
func (w WizardModel) Init() tea.Cmd {
	if len(w.steps) > 0 {
		return w.steps[0].Init()
	}
	return nil
}

// Skipper lets a step opt out of being shown for the current context — e.g.
// the image-generation toggle only applies to providers/models that can
// generate images. A skipped step is stepped over in BOTH directions (forward
// advance and esc/back), so it never strands navigation the way a step that
// self-completes in Init() would (that bounces esc straight back forward).
type Skipper interface {
	// Skip reports whether this step should be hidden for the given context.
	// Called with the context accumulated so far, so it can depend on earlier
	// steps' selections (e.g. the chosen provider + model).
	Skip(ctx *WizardContext) bool
}

// shouldSkip reports whether the step at index i opts out for the current ctx.
func (w *WizardModel) shouldSkip(i int) bool {
	if i < 0 || i >= len(w.steps) {
		return false
	}
	s, ok := w.steps[i].(Skipper)
	return ok && s.Skip(w.ctx)
}

// advanceStep applies the current step's data and moves to the next one,
// stepping over any skipped steps.
func (w *WizardModel) advanceStep() tea.Cmd {
	if w.current < len(w.steps) {
		w.steps[w.current].Apply(w.ctx)
	}

	w.current++
	for w.shouldSkip(w.current) {
		w.current++
	}
	if w.current >= len(w.steps) {
		w.done = true
		return tea.Quit
	}

	// Prepare the next step if it supports it
	if preparer, ok := w.steps[w.current].(interface{ Prepare(ctx *WizardContext) }); ok {
		preparer.Prepare(w.ctx)
	}
	return w.steps[w.current].Init()
}

// retreatStep moves back one step, stepping over any skipped steps. Returns
// false when there is no earlier step to land on (caller cancels).
func (w *WizardModel) retreatStep() bool {
	i := w.current - 1
	for i >= 0 && w.shouldSkip(i) {
		i--
	}
	if i < 0 {
		return false
	}
	w.current = i
	return true
}

// Update handles messages for the wizard.
func (w WizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		w.width = msg.Width
		w.height = msg.Height
		if w.current < len(w.steps) {
			updated, cmd := w.steps[w.current].Update(msg)
			w.steps[w.current] = updated
			return w, cmd
		}
		return w, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			w.err = fmt.Errorf("wizard cancelled")
			return w, tea.Quit
		}
		// esc is global back-navigation: return to the previous step (re-Init
		// so it starts fresh). Handled at the wizard level so it works from
		// EVERY step regardless of the step's own sub-phase or text inputs —
		// esc is never an editing key, so intercepting it here is safe. On
		// the first step there's nowhere to go back to, so esc cancels.
		if msg.String() == "esc" {
			if w.retreatStep() {
				return w, w.steps[w.current].Init()
			}
			w.err = fmt.Errorf("wizard cancelled")
			return w, tea.Quit
		}

	case StepBackMsg:
		if w.retreatStep() {
			return w, w.steps[w.current].Init()
		}
		return w, nil

	case StepCompleteMsg:
		// This is the sole path for step advancement.
		cmd := w.advanceStep()
		return w, cmd
	}

	// Delegate to current step
	if w.current < len(w.steps) {
		updated, cmd := w.steps[w.current].Update(msg)
		w.steps[w.current] = updated
		// Steps signal completion only via StepCompleteMsg — never check Complete() here.
		return w, cmd
	}

	return w, nil
}

// View renders the entire wizard UI.
func (w WizardModel) View() string {
	var out string

	// Banner
	out += "\n" + RenderBanner(w.styles, w.version, w.width)
	out += "\n"

	// Step progress (completed steps)
	out += RenderProgress(w.steps, w.current, w.styles, w.width)
	out += "\n"

	// Active step content
	if w.current < len(w.steps) {
		out += w.steps[w.current].View(w.width)
	}
	out += "\n"

	return out
}

// Context returns the accumulated wizard context.
func (w WizardModel) Context() *WizardContext {
	return w.ctx
}

// Err returns any error that occurred during the wizard.
func (w WizardModel) Err() error {
	return w.err
}

// Done returns true if the wizard completed successfully.
func (w WizardModel) Done() bool {
	return w.done
}
