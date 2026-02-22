package steps

import (
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

type toolsPhase int

const (
	toolsSelectPhase toolsPhase = iota
	toolsPerplexityKeyPhase
	toolsDonePhase
)

// ValidatePerplexityFunc validates a Perplexity API key.
type ValidatePerplexityFunc func(key string) error

// ToolsStep handles builtin tool selection.
type ToolsStep struct {
	styles        *tui.StyleSet
	phase         toolsPhase
	multiSelect   components.MultiSelect
	keyInput      components.SecretInput
	complete      bool
	selected      []string
	perplexityKey string
	validatePerp  ValidatePerplexityFunc
}

// NewToolsStep creates a new tools selection step.
func NewToolsStep(styles *tui.StyleSet, tools []ToolInfo, validatePerp ValidatePerplexityFunc) *ToolsStep {
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
		styles:       styles,
		multiSelect:  ms,
		validatePerp: validatePerp,
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

			// Check if web_search selected and no perplexity key
			if containsStr(s.selected, "web_search") && os.Getenv("PERPLEXITY_API_KEY") == "" {
				s.phase = toolsPerplexityKeyPhase
				s.keyInput = components.NewSecretInput(
					"Perplexity API key for web_search",
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

			s.complete = true
			return s, func() tea.Msg { return tui.StepCompleteMsg{} }
		}

		return s, cmd

	case toolsPerplexityKeyPhase:
		updated, cmd := s.keyInput.Update(msg)
		s.keyInput = updated

		if s.keyInput.Done() {
			s.perplexityKey = s.keyInput.Value()
			s.complete = true
			return s, func() tea.Msg { return tui.StepCompleteMsg{} }
		}

		return s, cmd
	}

	return s, nil
}

func (s *ToolsStep) View(width int) string {
	switch s.phase {
	case toolsSelectPhase:
		return s.multiSelect.View(width)
	case toolsPerplexityKeyPhase:
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
	if s.perplexityKey != "" {
		ctx.EnvVars["PERPLEXITY_API_KEY"] = s.perplexityKey
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
