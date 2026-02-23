package steps

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/initializ/forge/forge-cli/internal/tui"
	"github.com/initializ/forge/forge-cli/internal/tui/components"
)

// ToolInfo represents a builtin tool for the tools step.
type ToolInfo struct {
	Name        string
	Description string
}

// ValidateWebSearchKeyFunc validates a web search API key for a given provider.
type ValidateWebSearchKeyFunc func(provider, key string) error

type toolsPhase int

const (
	toolsSelectPhase toolsPhase = iota
	toolsWebSearchProviderPhase
	toolsWebSearchKeyPhase
	toolsWebSearchValidatingPhase
	toolsDonePhase
)

// ToolsStep handles builtin tool selection.
type ToolsStep struct {
	styles            *tui.StyleSet
	phase             toolsPhase
	multiSelect       components.MultiSelect
	providerSelect    components.SingleSelect
	keyInput          components.SecretInput
	complete          bool
	selected          []string
	webSearchKey      string
	webSearchKeyName  string // "TAVILY_API_KEY" or "PERPLEXITY_API_KEY"
	webSearchProvider string // "tavily" or "perplexity"
	validateFn        ValidateWebSearchKeyFunc
	validating        bool
}

// NewToolsStep creates a new tools selection step.
func NewToolsStep(styles *tui.StyleSet, tools []ToolInfo, validateFn ValidateWebSearchKeyFunc) *ToolsStep {
	var items []components.MultiSelectItem
	for _, t := range tools {
		icon := toolIcon(t.Name)
		items = append(items, components.MultiSelectItem{
			Label:       t.Name,
			Value:       t.Name,
			Description: t.Description,
			Icon:        icon,
		})
	}

	ms := components.NewMultiSelect(
		items,
		styles.Theme.Accent,
		styles.Theme.AccentDim,
		styles.Theme.Primary,
		styles.Theme.Secondary,
		styles.Theme.Dim,
		styles.ActiveBorder,
		styles.InactiveBorder,
		styles.KbdKey,
		styles.KbdDesc,
	)

	return &ToolsStep{
		styles:      styles,
		multiSelect: ms,
		validateFn:  validateFn,
	}
}

func (s *ToolsStep) Title() string { return "Built-in Tools" }
func (s *ToolsStep) Icon() string  { return "🔧" }

func (s *ToolsStep) Init() tea.Cmd {
	return s.multiSelect.Init()
}

func (s *ToolsStep) Update(msg tea.Msg) (tui.Step, tea.Cmd) {
	if s.complete {
		return s, nil
	}

	switch s.phase {
	case toolsSelectPhase:
		updated, cmd := s.multiSelect.Update(msg)
		s.multiSelect = updated

		if s.multiSelect.Done() {
			s.selected = s.multiSelect.SelectedValues()

			// Check if web_search selected and no key is already set
			if containsStr(s.selected, "web_search") &&
				os.Getenv("TAVILY_API_KEY") == "" &&
				os.Getenv("PERPLEXITY_API_KEY") == "" {
				// Show provider selection
				s.phase = toolsWebSearchProviderPhase
				s.providerSelect = components.NewSingleSelect(
					[]components.SingleSelectItem{
						{Label: "Tavily (Recommended)", Value: "tavily", Description: "LLM-optimized search with structured results", Icon: "🔍"},
						{Label: "Perplexity", Value: "perplexity", Description: "AI-powered search with citations", Icon: "🌐"},
					},
					s.styles.Theme.Accent,
					s.styles.Theme.Primary,
					s.styles.Theme.Secondary,
					s.styles.Theme.Dim,
					s.styles.Theme.Border,
					s.styles.Theme.Accent,
					s.styles.Theme.AccentDim,
					s.styles.KbdKey,
					s.styles.KbdDesc,
				)
				return s, s.providerSelect.Init()
			}

			// If a key is already set in env, detect the provider
			if containsStr(s.selected, "web_search") {
				if os.Getenv("TAVILY_API_KEY") != "" {
					s.webSearchProvider = "tavily"
				} else if os.Getenv("PERPLEXITY_API_KEY") != "" {
					s.webSearchProvider = "perplexity"
				}
			}

			s.complete = true
			return s, func() tea.Msg { return tui.StepCompleteMsg{} }
		}

		return s, cmd

	case toolsWebSearchProviderPhase:
		updated, cmd := s.providerSelect.Update(msg)
		s.providerSelect = updated

		if s.providerSelect.Done() {
			_, s.webSearchProvider = s.providerSelect.Selected()
			s.initKeyInput("")
			return s, s.keyInput.Init()
		}

		return s, cmd

	case toolsWebSearchKeyPhase:
		updated, cmd := s.keyInput.Update(msg)
		s.keyInput = updated

		if s.keyInput.Done() {
			s.webSearchKey = s.keyInput.Value()

			// Run validation if we have a key and a validateFn
			if s.webSearchKey != "" && s.validateFn != nil {
				s.phase = toolsWebSearchValidatingPhase
				s.validating = true
				return s, s.runValidation()
			}

			s.complete = true
			return s, func() tea.Msg { return tui.StepCompleteMsg{} }
		}

		return s, cmd

	case toolsWebSearchValidatingPhase:
		if msg, ok := msg.(tui.ValidationResultMsg); ok {
			s.validating = false
			if msg.Err != nil {
				// Validation failed — go back to key input with error
				s.initKeyInput(fmt.Sprintf("retry — %s", msg.Err))
				s.keyInput.SetState(components.SecretInputFailed, msg.Err.Error())
				return s, s.keyInput.Init()
			}
			// Success
			s.complete = true
			return s, func() tea.Msg { return tui.StepCompleteMsg{} }
		}

		return s, nil
	}

	return s, nil
}

// initKeyInput creates a fresh SecretInput for the web search API key.
func (s *ToolsStep) initKeyInput(suffix string) {
	keyLabel := "Tavily API key for web_search"
	s.webSearchKeyName = "TAVILY_API_KEY"
	if s.webSearchProvider == "perplexity" {
		keyLabel = "Perplexity API key for web_search"
		s.webSearchKeyName = "PERPLEXITY_API_KEY"
	}
	if suffix != "" {
		keyLabel = fmt.Sprintf("%s (%s)", keyLabel, suffix)
	}

	s.phase = toolsWebSearchKeyPhase
	s.keyInput = components.NewSecretInput(
		keyLabel,
		false, // required — cannot skip
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
}

// runValidation runs the web search key validation asynchronously.
func (s *ToolsStep) runValidation() tea.Cmd {
	provider := s.webSearchProvider
	key := s.webSearchKey
	validateFn := s.validateFn
	return func() tea.Msg {
		if validateFn == nil {
			return tui.ValidationResultMsg{Err: nil}
		}
		err := validateFn(provider, key)
		return tui.ValidationResultMsg{Err: err}
	}
}

func (s *ToolsStep) View(width int) string {
	switch s.phase {
	case toolsSelectPhase:
		return s.multiSelect.View(width)
	case toolsWebSearchProviderPhase:
		return s.providerSelect.View(width)
	case toolsWebSearchKeyPhase:
		return s.keyInput.View(width)
	case toolsWebSearchValidatingPhase:
		if s.validating {
			return "  " + s.styles.AccentTxt.Render("⣾ Validating...") + "\n"
		}
		return s.keyInput.View(width)
	}
	return ""
}

func (s *ToolsStep) Complete() bool {
	return s.complete
}

func (s *ToolsStep) Summary() string {
	if len(s.selected) == 0 {
		return "none"
	}
	return strings.Join(s.selected, ", ")
}

func (s *ToolsStep) Apply(ctx *tui.WizardContext) {
	ctx.BuiltinTools = s.selected
	if s.webSearchKey != "" && s.webSearchKeyName != "" {
		ctx.EnvVars[s.webSearchKeyName] = s.webSearchKey
	}
	if s.webSearchProvider != "" {
		ctx.EnvVars["WEB_SEARCH_PROVIDER"] = s.webSearchProvider
	}
}

func toolIcon(name string) string {
	icons := map[string]string{
		"http_request":   "🌐",
		"json_parse":     "📋",
		"csv_parse":      "📊",
		"datetime_now":   "🕐",
		"uuid_generate":  "🔑",
		"math_calculate": "🔢",
		"web_search":     "🔍",
	}
	if icon, ok := icons[name]; ok {
		return icon
	}
	return "🔧"
}

func containsStr(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}
