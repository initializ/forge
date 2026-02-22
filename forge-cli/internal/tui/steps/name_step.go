package steps

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/initializ/forge/forge-cli/internal/tui"
	"github.com/initializ/forge/forge-cli/internal/tui/components"
)

// NameStep collects the agent name.
type NameStep struct {
	input    components.TextInput
	complete bool
	name     string
	prefill  string
}

// NewNameStep creates a new name step.
func NewNameStep(styles *tui.StyleSet, prefill string) *NameStep {
	validate := func(val string) error {
		if val == "" {
			return fmt.Errorf("name is required")
		}
		return nil
	}

	input := components.NewTextInput(
		"What should we call your agent?",
		"my-agent",
		true, // show slug hint
		validate,
		styles.Theme.Accent,
		styles.AccentTxt,
		styles.InactiveBorder,
		styles.ErrorTxt,
		styles.DimTxt,
		styles.KbdKey,
		styles.KbdDesc,
	)

	if prefill != "" {
		input.SetValue(prefill)
	}

	return &NameStep{
		input:   input,
		prefill: prefill,
	}
}

func (s *NameStep) Title() string { return "Agent Name" }
func (s *NameStep) Icon() string  { return "📝" }

func (s *NameStep) Init() tea.Cmd {
	// If pre-filled, auto-complete
	if s.prefill != "" {
		s.complete = true
		s.name = s.prefill
		return func() tea.Msg { return tui.StepCompleteMsg{} }
	}
	return s.input.Init()
}

func (s *NameStep) Update(msg tea.Msg) (tui.Step, tea.Cmd) {
	if s.complete {
		return s, nil
	}

	updated, cmd := s.input.Update(msg)
	s.input = updated

	if s.input.Done() {
		s.complete = true
		s.name = s.input.Value()
		return s, func() tea.Msg { return tui.StepCompleteMsg{} }
	}

	return s, cmd
}

func (s *NameStep) View(width int) string {
	return s.input.View(width)
}

func (s *NameStep) Complete() bool {
	return s.complete
}

func (s *NameStep) Summary() string {
	return s.name
}

func (s *NameStep) Apply(ctx *tui.WizardContext) {
	ctx.Name = s.name
}
