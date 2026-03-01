package steps

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/initializ/forge/forge-cli/internal/tui"
	"github.com/initializ/forge/forge-cli/internal/tui/components"
)

type fallbackPhase int

const (
	fallbackAskPhase fallbackPhase = iota
	fallbackSelectPhase
	fallbackKeyPhase
	fallbackDonePhase
)

// FallbackStep handles fallback provider selection and API key collection.
type FallbackStep struct {
	styles        *tui.StyleSet
	phase         fallbackPhase
	askSelector   components.SingleSelect
	multiSelector components.MultiSelect
	keyInput      components.SecretInput
	complete      bool
	primaryProv   string
	selected      []string // providers selected by user
	collected     []tui.FallbackProvider
	keyIndex      int // which selected provider we're collecting a key for
	validateFn    ValidateKeyFunc
	validating    bool
}

// NewFallbackStep creates a new fallback provider wizard step.
func NewFallbackStep(styles *tui.StyleSet, validateFn ValidateKeyFunc) *FallbackStep {
	return &FallbackStep{
		styles:     styles,
		validateFn: validateFn,
	}
}

// Prepare is called by the wizard to provide the primary provider context.
func (s *FallbackStep) Prepare(ctx *tui.WizardContext) {
	s.primaryProv = ctx.Provider
	s.complete = false
	s.phase = fallbackAskPhase
	s.selected = nil
	s.collected = nil
	s.keyIndex = 0
}

func (s *FallbackStep) Title() string { return "Fallback Providers" }
func (s *FallbackStep) Icon() string  { return "🔄" }

func (s *FallbackStep) Init() tea.Cmd {
	// Build the "Add fallback providers?" selector
	items := []components.SingleSelectItem{
		{Label: "No", Value: "no", Description: "Use only the primary provider", Icon: "⏭️"},
		{Label: "Yes", Value: "yes", Description: "Configure fallback providers for reliability", Icon: "✅"},
	}
	s.askSelector = components.NewSingleSelect(
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
	return s.askSelector.Init()
}

func (s *FallbackStep) Update(msg tea.Msg) (tui.Step, tea.Cmd) {
	if s.complete {
		return s, nil
	}

	switch s.phase {
	case fallbackAskPhase:
		return s.updateAskPhase(msg)
	case fallbackSelectPhase:
		return s.updateSelectPhase(msg)
	case fallbackKeyPhase:
		return s.updateKeyPhase(msg)
	}

	return s, nil
}

func (s *FallbackStep) updateAskPhase(msg tea.Msg) (tui.Step, tea.Cmd) {
	// Handle backspace for back navigation
	if msg, ok := msg.(tea.KeyMsg); ok && msg.String() == "backspace" {
		return s, func() tea.Msg { return tui.StepBackMsg{} }
	}

	updated, cmd := s.askSelector.Update(msg)
	s.askSelector = updated

	if s.askSelector.Done() {
		_, val := s.askSelector.Selected()
		if val == "no" {
			s.complete = true
			return s, func() tea.Msg { return tui.StepCompleteMsg{} }
		}
		// Yes — show multi-select of providers
		s.phase = fallbackSelectPhase
		s.buildMultiSelect()
		return s, s.multiSelector.Init()
	}

	return s, cmd
}

func (s *FallbackStep) buildMultiSelect() {
	allProviders := []struct {
		label, value, desc, icon string
	}{
		{"OpenAI", "openai", "GPT-4o, GPT-4o-mini", "🔷"},
		{"Anthropic", "anthropic", "Claude Sonnet, Haiku, Opus", "🟠"},
		{"Google Gemini", "gemini", "Gemini 2.5 Flash, Pro", "🔵"},
		{"Ollama (local)", "ollama", "Run models locally, no API key needed", "🦙"},
	}

	var items []components.MultiSelectItem
	for _, p := range allProviders {
		if p.value == s.primaryProv {
			continue // exclude primary
		}
		items = append(items, components.MultiSelectItem{
			Label:       p.label,
			Value:       p.value,
			Description: p.desc,
			Icon:        p.icon,
		})
	}

	s.multiSelector = components.NewMultiSelect(
		items,
		s.styles.Theme.Accent,
		s.styles.Theme.AccentDim,
		s.styles.Theme.Primary,
		s.styles.Theme.Secondary,
		s.styles.Theme.Dim,
		s.styles.ActiveBorder,
		s.styles.InactiveBorder,
		s.styles.KbdKey,
		s.styles.KbdDesc,
	)
}

func (s *FallbackStep) updateSelectPhase(msg tea.Msg) (tui.Step, tea.Cmd) {
	// Handle backspace to go back to ask phase
	if msg, ok := msg.(tea.KeyMsg); ok && msg.String() == "backspace" {
		s.phase = fallbackAskPhase
		s.askSelector.Reset()
		return s, s.askSelector.Init()
	}

	updated, cmd := s.multiSelector.Update(msg)
	s.multiSelector = updated

	if s.multiSelector.Done() {
		s.selected = s.multiSelector.SelectedValues()
		if len(s.selected) == 0 {
			// No providers selected — skip
			s.complete = true
			return s, func() tea.Msg { return tui.StepCompleteMsg{} }
		}
		// Start collecting API keys
		s.keyIndex = 0
		return s.advanceToNextKey()
	}

	return s, cmd
}

func (s *FallbackStep) advanceToNextKey() (tui.Step, tea.Cmd) {
	// Skip providers that don't need keys
	for s.keyIndex < len(s.selected) {
		if s.selected[s.keyIndex] == "ollama" {
			s.collected = append(s.collected, tui.FallbackProvider{
				Provider: "ollama",
			})
			s.keyIndex++
			continue
		}
		break
	}

	if s.keyIndex >= len(s.selected) {
		// All keys collected
		s.complete = true
		return s, func() tea.Msg { return tui.StepCompleteMsg{} }
	}

	provider := s.selected[s.keyIndex]
	s.phase = fallbackKeyPhase
	label := fmt.Sprintf("%s API Key (fallback)", providerDisplayName(provider))
	s.keyInput = components.NewSecretInput(
		label, true, true,
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

func (s *FallbackStep) updateKeyPhase(msg tea.Msg) (tui.Step, tea.Cmd) {
	// Handle validation result
	if msg, ok := msg.(tui.ValidationResultMsg); ok {
		s.validating = false
		provider := s.selected[s.keyIndex]
		if msg.Err != nil {
			// Retry key input
			label := fmt.Sprintf("%s API Key (retry — %s)", providerDisplayName(provider), msg.Err)
			s.keyInput = components.NewSecretInput(
				label, true, true,
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
		// Success
		s.collected = append(s.collected, tui.FallbackProvider{
			Provider: provider,
			APIKey:   s.keyInput.Value(),
		})
		s.keyIndex++
		return s.advanceToNextKey()
	}

	// Handle backspace at empty to go back
	if msg, ok := msg.(tea.KeyMsg); ok && msg.String() == "backspace" {
		if s.keyInput.Value() == "" {
			s.phase = fallbackSelectPhase
			s.multiSelector.Reset()
			s.collected = nil
			s.keyIndex = 0
			return s, s.multiSelector.Init()
		}
	}

	updated, cmd := s.keyInput.Update(msg)
	s.keyInput = updated

	if s.keyInput.Done() {
		key := s.keyInput.Value()
		provider := s.selected[s.keyIndex]
		if key == "" {
			// Skipped — add without key
			s.collected = append(s.collected, tui.FallbackProvider{
				Provider: provider,
			})
			s.keyIndex++
			return s.advanceToNextKey()
		}
		// Validate the key
		if s.validateFn != nil {
			s.validating = true
			validateFn := s.validateFn
			return s, func() tea.Msg {
				err := validateFn(provider, key)
				return tui.ValidationResultMsg{Err: err}
			}
		}
		// No validation function — accept
		s.collected = append(s.collected, tui.FallbackProvider{
			Provider: provider,
			APIKey:   key,
		})
		s.keyIndex++
		return s.advanceToNextKey()
	}

	return s, cmd
}

func (s *FallbackStep) View(width int) string {
	switch s.phase {
	case fallbackAskPhase:
		return s.askSelector.View(width)
	case fallbackSelectPhase:
		return s.multiSelector.View(width)
	case fallbackKeyPhase:
		if s.validating {
			return "  " + s.styles.AccentTxt.Render("⣾ Validating...") + "\n"
		}
		return s.keyInput.View(width)
	}
	return ""
}

func (s *FallbackStep) Complete() bool {
	return s.complete
}

func (s *FallbackStep) Summary() string {
	if len(s.collected) == 0 {
		return "none"
	}
	var names []string
	for _, fb := range s.collected {
		names = append(names, providerDisplayName(fb.Provider))
	}
	return strings.Join(names, ", ")
}

func (s *FallbackStep) Apply(ctx *tui.WizardContext) {
	ctx.Fallbacks = s.collected

	// Store fallback provider keys in env vars
	for _, fb := range s.collected {
		if fb.APIKey == "" {
			continue
		}
		switch fb.Provider {
		case "openai":
			ctx.EnvVars["OPENAI_API_KEY"] = fb.APIKey
		case "anthropic":
			ctx.EnvVars["ANTHROPIC_API_KEY"] = fb.APIKey
		case "gemini":
			ctx.EnvVars["GEMINI_API_KEY"] = fb.APIKey
		}
	}
}
