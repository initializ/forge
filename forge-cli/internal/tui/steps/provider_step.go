package steps

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/initializ/forge/forge-cli/internal/tui"
	"github.com/initializ/forge/forge-cli/internal/tui/components"
)

type providerPhase int

const (
	providerSelectPhase providerPhase = iota
	providerAuthMethodPhase
	providerKeyPhase
	providerValidatingPhase
	providerOAuthPhase
	providerModelPhase
	providerCustomURLPhase
	providerCustomModelPhase
	providerCustomAuthPhase
	providerDonePhase
)

// OAuthFlowFunc is a function that runs the OAuth flow and returns the access token.
type OAuthFlowFunc func(provider string) (accessToken string, err error)

// ValidateKeyFunc validates an API key for a provider.
type ValidateKeyFunc func(provider, key string) error

// modelOption maps a user-friendly display name to the actual model ID.
type modelOption struct {
	DisplayName string
	ModelID     string
}

// openAIOAuthModels are available when using browser-based OAuth login.
var openAIOAuthModels = []modelOption{
	{DisplayName: "GPT 5.3 Codex", ModelID: "gpt-5.3-codex"},
	{DisplayName: "GPT 5.2", ModelID: "gpt-5.2-2025-12-11"},
	{DisplayName: "GPT 5.2 Codex", ModelID: "gpt-5.2-codex"},
}

// openAIAPIKeyModels are available when using an API key.
var openAIAPIKeyModels = []modelOption{
	{DisplayName: "GPT 5.2", ModelID: "gpt-5.2-2025-12-11"},
	{DisplayName: "GPT 5 Mini", ModelID: "gpt-5-mini-2025-08-07"},
	{DisplayName: "GPT 5 Nano", ModelID: "gpt-5-nano-2025-08-07"},
	{DisplayName: "GPT 4.1 Mini", ModelID: "gpt-4.1-mini-2025-04-14"},
}

// ProviderStep handles model provider selection and API key entry.
type ProviderStep struct {
	styles             *tui.StyleSet
	phase              providerPhase
	selector           components.SingleSelect
	authMethodSelector components.SingleSelect
	modelSelector      components.SingleSelect
	keyInput           components.SecretInput
	textInput          components.TextInput
	complete           bool
	provider           string
	apiKey             string
	authMethod         string // "apikey" or "oauth"
	modelID            string // selected model ID
	customURL          string
	customModel        string
	customAuth         string
	validateFn         ValidateKeyFunc
	oauthFn            OAuthFlowFunc
	validating         bool
	valErr             error
	oauthRunning       bool
}

// NewProviderStep creates a new provider selection step.
// oauthFn is optional — pass nil to disable OAuth login.
func NewProviderStep(styles *tui.StyleSet, validateFn ValidateKeyFunc, oauthFn ...OAuthFlowFunc) *ProviderStep {
	items := []components.SingleSelectItem{
		{Label: "OpenAI", Value: "openai", Description: "GPT 5.3 Codex, GPT 5.2, GPT 5 Mini", Icon: "🔷"},
		{Label: "Anthropic", Value: "anthropic", Description: "Claude Sonnet, Haiku, Opus", Icon: "🟠"},
		{Label: "Google Gemini", Value: "gemini", Description: "Gemini 2.5 Flash, Pro", Icon: "🔵"},
		{Label: "Ollama (local)", Value: "ollama", Description: "Run models locally, no API key needed", Icon: "🦙"},
		{Label: "Custom URL", Value: "custom", Description: "Any OpenAI-compatible endpoint", Icon: "⚙️"},
	}

	selector := components.NewSingleSelect(
		items,
		styles.Theme.Accent,
		styles.Theme.Primary,
		styles.Theme.Secondary,
		styles.Theme.Dim,
		styles.Theme.Border,
		styles.Theme.ActiveBorder,
		styles.Theme.ActiveBg,
		styles.KbdKey,
		styles.KbdDesc,
	)

	var oFn OAuthFlowFunc
	if len(oauthFn) > 0 {
		oFn = oauthFn[0]
	}

	return &ProviderStep{
		styles:     styles,
		selector:   selector,
		validateFn: validateFn,
		oauthFn:    oFn,
	}
}

func (s *ProviderStep) Title() string { return "Model Provider" }
func (s *ProviderStep) Icon() string  { return "🤖" }

func (s *ProviderStep) Init() tea.Cmd {
	return s.selector.Init()
}

func (s *ProviderStep) Update(msg tea.Msg) (tui.Step, tea.Cmd) {
	if s.complete {
		return s, nil
	}

	switch s.phase {
	case providerSelectPhase:
		return s.updateSelectPhase(msg)
	case providerAuthMethodPhase:
		return s.updateAuthMethodPhase(msg)
	case providerKeyPhase:
		return s.updateKeyPhase(msg)
	case providerValidatingPhase:
		return s.updateValidatingPhase(msg)
	case providerOAuthPhase:
		return s.updateOAuthPhase(msg)
	case providerModelPhase:
		return s.updateModelPhase(msg)
	case providerCustomURLPhase:
		return s.updateCustomURLPhase(msg)
	case providerCustomModelPhase:
		return s.updateCustomModelPhase(msg)
	case providerCustomAuthPhase:
		return s.updateCustomAuthPhase(msg)
	}

	return s, nil
}

func (s *ProviderStep) updateSelectPhase(msg tea.Msg) (tui.Step, tea.Cmd) {
	updated, cmd := s.selector.Update(msg)
	s.selector = updated

	if s.selector.Done() {
		_, val := s.selector.Selected()
		s.provider = val

		switch val {
		case "ollama":
			// Skip key, go to validation
			s.phase = providerValidatingPhase
			s.validating = true
			return s, s.runValidation()
		case "custom":
			s.phase = providerCustomURLPhase
			s.textInput = components.NewTextInput(
				"Base URL (e.g. http://localhost:11434/v1)",
				"http://localhost:11434/v1",
				false, nil,
				s.styles.Theme.Accent,
				s.styles.AccentTxt,
				s.styles.InactiveBorder,
				s.styles.ErrorTxt,
				s.styles.DimTxt,
				s.styles.KbdKey,
				s.styles.KbdDesc,
			)
			return s, s.textInput.Init()
		case "openai":
			// If OAuth is available, show auth method choice
			if s.oauthFn != nil {
				s.phase = providerAuthMethodPhase
				items := []components.SingleSelectItem{
					{Label: "Enter API Key", Value: "apikey", Description: "Paste your OpenAI API key", Icon: "🔑"},
					{Label: "Login with OpenAI", Value: "oauth", Description: "Browser-based login (OAuth)", Icon: "🌐"},
				}
				s.authMethodSelector = components.NewSingleSelect(
					items,
					s.styles.Theme.Accent,
					s.styles.Theme.Primary,
					s.styles.Theme.Secondary,
					s.styles.Theme.Dim,
					s.styles.Theme.Border,
					s.styles.Theme.ActiveBorder,
					s.styles.Theme.ActiveBg,
					s.styles.KbdKey,
					s.styles.KbdDesc,
				)
				return s, s.authMethodSelector.Init()
			}
			// No OAuth — fall through to API key
			s.authMethod = "apikey"
			fallthrough
		default:
			// openai, anthropic, gemini → ask for key
			s.phase = providerKeyPhase
			label := fmt.Sprintf("%s API Key", providerDisplayName(val))
			s.keyInput = components.NewSecretInput(
				label, true,
				s.styles.Theme.Accent,
				s.styles.Theme.Success,
				s.styles.Theme.Error,
				s.styles.Theme.Border,
				s.styles.AccentTxt,
				s.styles.InactiveBorder,
				s.styles.SuccessTxt,
				s.styles.ErrorTxt,
				s.styles.DimTxt,
				s.styles.KbdKey,
				s.styles.KbdDesc,
			)
			return s, s.keyInput.Init()
		}
	}

	return s, cmd
}

func (s *ProviderStep) updateAuthMethodPhase(msg tea.Msg) (tui.Step, tea.Cmd) {
	// Handle backspace to go back to provider selector
	if msg, ok := msg.(tea.KeyMsg); ok && msg.String() == "backspace" {
		s.phase = providerSelectPhase
		s.provider = ""
		s.selector.Reset()
		return s, s.selector.Init()
	}

	updated, cmd := s.authMethodSelector.Update(msg)
	s.authMethodSelector = updated

	if s.authMethodSelector.Done() {
		_, val := s.authMethodSelector.Selected()
		s.authMethod = val
		if val == "oauth" {
			// Run OAuth flow
			s.phase = providerOAuthPhase
			s.oauthRunning = true
			oauthFn := s.oauthFn
			return s, func() tea.Msg {
				_, err := oauthFn("openai")
				return tui.ValidationResultMsg{Err: err}
			}
		}
		// API key method
		s.phase = providerKeyPhase
		label := fmt.Sprintf("%s API Key", providerDisplayName(s.provider))
		s.keyInput = components.NewSecretInput(
			label, true,
			s.styles.Theme.Accent,
			s.styles.Theme.Success,
			s.styles.Theme.Error,
			s.styles.Theme.Border,
			s.styles.AccentTxt,
			s.styles.InactiveBorder,
			s.styles.SuccessTxt,
			s.styles.ErrorTxt,
			s.styles.DimTxt,
			s.styles.KbdKey,
			s.styles.KbdDesc,
		)
		return s, s.keyInput.Init()
	}

	return s, cmd
}

func (s *ProviderStep) updateOAuthPhase(msg tea.Msg) (tui.Step, tea.Cmd) {
	if msg, ok := msg.(tui.ValidationResultMsg); ok {
		s.oauthRunning = false
		if msg.Err != nil {
			// OAuth failed — fall back to API key entry
			s.phase = providerKeyPhase
			label := fmt.Sprintf("%s API Key (OAuth failed — %s)", providerDisplayName(s.provider), msg.Err)
			s.keyInput = components.NewSecretInput(
				label, true,
				s.styles.Theme.Accent,
				s.styles.Theme.Success,
				s.styles.Theme.Error,
				s.styles.Theme.Border,
				s.styles.AccentTxt,
				s.styles.InactiveBorder,
				s.styles.SuccessTxt,
				s.styles.ErrorTxt,
				s.styles.DimTxt,
				s.styles.KbdKey,
				s.styles.KbdDesc,
			)
			return s, s.keyInput.Init()
		}
		// OAuth succeeded — show model selection
		s.apiKey = "__oauth__"
		return s, s.showModelSelector()
	}

	return s, nil
}

func (s *ProviderStep) updateKeyPhase(msg tea.Msg) (tui.Step, tea.Cmd) {
	// Handle backspace at empty input → go back to provider selector (internal back)
	if msg, ok := msg.(tea.KeyMsg); ok && msg.String() == "backspace" {
		if s.keyInput.Value() == "" {
			s.phase = providerSelectPhase
			s.provider = ""
			s.selector.Reset()
			return s, s.selector.Init()
		}
	}

	updated, cmd := s.keyInput.Update(msg)
	s.keyInput = updated

	if s.keyInput.Done() {
		s.apiKey = s.keyInput.Value()
		if s.apiKey == "" {
			// Skipped validation
			s.complete = true
			return s, func() tea.Msg { return tui.StepCompleteMsg{} }
		}
		// Validate
		s.phase = providerValidatingPhase
		s.validating = true
		return s, s.runValidation()
	}

	return s, cmd
}

func (s *ProviderStep) updateValidatingPhase(msg tea.Msg) (tui.Step, tea.Cmd) {
	if msg, ok := msg.(tui.ValidationResultMsg); ok {
		s.validating = false
		if msg.Err != nil {
			s.valErr = msg.Err
			// Go back to key input on failure — create fresh input for retry
			if s.provider != "ollama" {
				s.phase = providerKeyPhase
				label := fmt.Sprintf("%s API Key (retry — %s)", providerDisplayName(s.provider), msg.Err)
				s.keyInput = components.NewSecretInput(
					label, true,
					s.styles.Theme.Accent,
					s.styles.Theme.Success,
					s.styles.Theme.Error,
					s.styles.Theme.Border,
					s.styles.AccentTxt,
					s.styles.InactiveBorder,
					s.styles.SuccessTxt,
					s.styles.ErrorTxt,
					s.styles.DimTxt,
					s.styles.KbdKey,
					s.styles.KbdDesc,
				)
				s.keyInput.SetState(components.SecretInputFailed, msg.Err.Error())
				return s, s.keyInput.Init()
			}
			// For ollama, warn but continue
			s.complete = true
			return s, func() tea.Msg { return tui.StepCompleteMsg{} }
		}
		// Validation passed — show model selection for OpenAI
		if s.provider == "openai" {
			return s, s.showModelSelector()
		}
		s.complete = true
		return s, func() tea.Msg { return tui.StepCompleteMsg{} }
	}

	return s, nil
}

// showModelSelector sets up the model selection phase for OpenAI.
func (s *ProviderStep) showModelSelector() tea.Cmd {
	var models []modelOption
	if s.authMethod == "oauth" {
		models = openAIOAuthModels
	} else {
		models = openAIAPIKeyModels
	}

	items := make([]components.SingleSelectItem, len(models))
	for i, m := range models {
		items[i] = components.SingleSelectItem{
			Label: m.DisplayName,
			Value: m.ModelID,
		}
	}

	s.modelSelector = components.NewSingleSelect(
		items,
		s.styles.Theme.Accent,
		s.styles.Theme.Primary,
		s.styles.Theme.Secondary,
		s.styles.Theme.Dim,
		s.styles.Theme.Border,
		s.styles.Theme.ActiveBorder,
		s.styles.Theme.ActiveBg,
		s.styles.KbdKey,
		s.styles.KbdDesc,
	)
	s.phase = providerModelPhase
	return s.modelSelector.Init()
}

func (s *ProviderStep) updateModelPhase(msg tea.Msg) (tui.Step, tea.Cmd) {
	updated, cmd := s.modelSelector.Update(msg)
	s.modelSelector = updated

	if s.modelSelector.Done() {
		_, val := s.modelSelector.Selected()
		s.modelID = val
		s.complete = true
		return s, func() tea.Msg { return tui.StepCompleteMsg{} }
	}

	return s, cmd
}

func (s *ProviderStep) updateCustomURLPhase(msg tea.Msg) (tui.Step, tea.Cmd) {
	updated, cmd := s.textInput.Update(msg)
	s.textInput = updated

	if s.textInput.Done() {
		s.customURL = s.textInput.Value()
		s.phase = providerCustomModelPhase
		s.textInput = components.NewTextInput(
			"Model name",
			"default",
			false, nil,
			s.styles.Theme.Accent,
			s.styles.AccentTxt,
			s.styles.InactiveBorder,
			s.styles.ErrorTxt,
			s.styles.DimTxt,
			s.styles.KbdKey,
			s.styles.KbdDesc,
		)
		return s, s.textInput.Init()
	}

	return s, cmd
}

func (s *ProviderStep) updateCustomModelPhase(msg tea.Msg) (tui.Step, tea.Cmd) {
	updated, cmd := s.textInput.Update(msg)
	s.textInput = updated

	if s.textInput.Done() {
		s.customModel = s.textInput.Value()
		s.phase = providerCustomAuthPhase
		s.keyInput = components.NewSecretInput(
			"API key or auth token (optional)",
			true,
			s.styles.Theme.Accent,
			s.styles.Theme.Success,
			s.styles.Theme.Error,
			s.styles.Theme.Border,
			s.styles.AccentTxt,
			s.styles.InactiveBorder,
			s.styles.SuccessTxt,
			s.styles.ErrorTxt,
			s.styles.DimTxt,
			s.styles.KbdKey,
			s.styles.KbdDesc,
		)
		return s, s.keyInput.Init()
	}

	return s, cmd
}

func (s *ProviderStep) updateCustomAuthPhase(msg tea.Msg) (tui.Step, tea.Cmd) {
	updated, cmd := s.keyInput.Update(msg)
	s.keyInput = updated

	if s.keyInput.Done() {
		s.customAuth = s.keyInput.Value()
		s.complete = true
		return s, func() tea.Msg { return tui.StepCompleteMsg{} }
	}

	return s, cmd
}

func (s *ProviderStep) runValidation() tea.Cmd {
	provider := s.provider
	key := s.apiKey
	validateFn := s.validateFn
	return func() tea.Msg {
		if validateFn == nil {
			return tui.ValidationResultMsg{Err: nil}
		}
		err := validateFn(provider, key)
		return tui.ValidationResultMsg{Err: err}
	}
}

func (s *ProviderStep) View(width int) string {
	switch s.phase {
	case providerSelectPhase:
		return s.selector.View(width)
	case providerAuthMethodPhase:
		return s.authMethodSelector.View(width)
	case providerKeyPhase:
		return s.keyInput.View(width)
	case providerValidatingPhase:
		if s.validating {
			return "  " + s.styles.AccentTxt.Render("⣾ Validating...") + "\n"
		}
		return s.keyInput.View(width)
	case providerOAuthPhase:
		if s.oauthRunning {
			return "  " + s.styles.AccentTxt.Render("⣾ Waiting for browser authorization...") + "\n"
		}
		return ""
	case providerModelPhase:
		return s.modelSelector.View(width)
	case providerCustomURLPhase, providerCustomModelPhase:
		return s.textInput.View(width)
	case providerCustomAuthPhase:
		return s.keyInput.View(width)
	}
	return ""
}

func (s *ProviderStep) Complete() bool {
	return s.complete
}

func (s *ProviderStep) Summary() string {
	name := providerDisplayName(s.provider)
	if s.modelID != "" {
		return name + " · " + modelDisplayName(s.modelID)
	}
	switch s.provider {
	case "openai":
		return name + " · GPT 5.2"
	case "anthropic":
		return name + " · Claude Sonnet 4"
	case "gemini":
		return name + " · Gemini 2.5 Flash"
	case "ollama":
		return name + " · llama3"
	case "custom":
		if s.customModel != "" {
			return "Custom · " + s.customModel
		}
		return "Custom URL"
	}
	return name
}

func (s *ProviderStep) Apply(ctx *tui.WizardContext) {
	ctx.Provider = s.provider
	ctx.APIKey = s.apiKey
	ctx.AuthMethod = s.authMethod
	ctx.ModelName = s.modelID
	ctx.CustomBaseURL = s.customURL
	ctx.CustomModel = s.customModel
	ctx.CustomAPIKey = s.customAuth

	// Store the provider API key in EnvVars so later steps (e.g. skills)
	// can detect it's already collected and skip re-prompting.
	if s.apiKey != "" {
		switch s.provider {
		case "openai":
			ctx.EnvVars["OPENAI_API_KEY"] = s.apiKey
		case "anthropic":
			ctx.EnvVars["ANTHROPIC_API_KEY"] = s.apiKey
		case "gemini":
			ctx.EnvVars["GEMINI_API_KEY"] = s.apiKey
		}
	}
}

// modelDisplayName returns the user-friendly name for a model ID.
func modelDisplayName(modelID string) string {
	// Check all model lists
	for _, m := range openAIOAuthModels {
		if m.ModelID == modelID {
			return m.DisplayName
		}
	}
	for _, m := range openAIAPIKeyModels {
		if m.ModelID == modelID {
			return m.DisplayName
		}
	}
	return modelID
}

func providerDisplayName(provider string) string {
	switch provider {
	case "openai":
		return "OpenAI"
	case "anthropic":
		return "Anthropic"
	case "gemini":
		return "Google Gemini"
	case "ollama":
		return "Ollama"
	case "custom":
		return "Custom"
	}
	return provider
}
