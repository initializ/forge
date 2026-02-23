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
	providerKeyPhase
	providerValidatingPhase
	providerCustomURLPhase
	providerCustomModelPhase
	providerCustomAuthPhase
	providerDonePhase
)

// ValidateKeyFunc validates an API key for a provider.
type ValidateKeyFunc func(provider, key string) error

// ProviderStep handles model provider selection and API key entry.
type ProviderStep struct {
	styles      *tui.StyleSet
	phase       providerPhase
	selector    components.SingleSelect
	keyInput    components.SecretInput
	textInput   components.TextInput
	complete    bool
	provider    string
	apiKey      string
	customURL   string
	customModel string
	customAuth  string
	validateFn  ValidateKeyFunc
	validating  bool
	valErr      error
}

// NewProviderStep creates a new provider selection step.
func NewProviderStep(styles *tui.StyleSet, validateFn ValidateKeyFunc) *ProviderStep {
	items := []components.SingleSelectItem{
		{Label: "OpenAI", Value: "openai", Description: "GPT-4o, GPT-4o-mini", Icon: "🔷"},
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

	return &ProviderStep{
		styles:     styles,
		selector:   selector,
		validateFn: validateFn,
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
	case providerKeyPhase:
		return s.updateKeyPhase(msg)
	case providerValidatingPhase:
		return s.updateValidatingPhase(msg)
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
		s.complete = true
		return s, func() tea.Msg { return tui.StepCompleteMsg{} }
	}

	return s, nil
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
	case providerKeyPhase:
		return s.keyInput.View(width)
	case providerValidatingPhase:
		if s.validating {
			return "  " + s.styles.AccentTxt.Render("⣾ Validating...") + "\n"
		}
		return s.keyInput.View(width)
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
	switch s.provider {
	case "openai":
		return name + " · gpt-4o-mini"
	case "anthropic":
		return name + " · claude-sonnet-4-20250514"
	case "gemini":
		return name + " · gemini-2.5-flash"
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
