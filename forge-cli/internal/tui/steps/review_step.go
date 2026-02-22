package steps

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/initializ/forge/forge-cli/internal/tui"
	"github.com/initializ/forge/forge-cli/internal/tui/components"
)

// ReviewStep handles the final summary and confirmation.
// Actual scaffolding is handled by the caller after the wizard exits.
type ReviewStep struct {
	styles   *tui.StyleSet
	summary  components.SummaryBox
	complete bool
	kbd      components.KbdHint
	prepared bool
}

// NewReviewStep creates a new review step.
func NewReviewStep(styles *tui.StyleSet) *ReviewStep {
	kbd := components.NewKbdHint(styles.KbdKey, styles.KbdDesc)
	kbd.Bindings = components.ReviewHints()

	return &ReviewStep{
		styles: styles,
		kbd:    kbd,
	}
}

// Prepare builds the summary from wizard context.
func (s *ReviewStep) Prepare(ctx *tui.WizardContext) {
	s.prepared = true
	s.complete = false

	var rows []components.SummaryRow
	rows = append(rows, components.SummaryRow{Key: "Name", Value: ctx.Name})
	rows = append(rows, components.SummaryRow{Key: "Provider", Value: providerDisplayName(ctx.Provider)})

	if ctx.Channel != "" && ctx.Channel != "none" {
		rows = append(rows, components.SummaryRow{Key: "Channel", Value: ctx.Channel})
	}

	if len(ctx.BuiltinTools) > 0 {
		rows = append(rows, components.SummaryRow{Key: "Tools", Value: strings.Join(ctx.BuiltinTools, ", ")})
	}

	if len(ctx.Skills) > 0 {
		rows = append(rows, components.SummaryRow{Key: "Skills", Value: strings.Join(ctx.Skills, ", ")})
	}

	if len(ctx.EgressDomains) > 0 {
		rows = append(rows, components.SummaryRow{Key: "Egress", Value: fmt.Sprintf("restricted · %d domains", len(ctx.EgressDomains))})
	}

	s.summary = components.NewSummaryBox(
		rows,
		s.styles.SummaryKey,
		s.styles.SummaryValue,
		s.styles.BorderedBox,
	)
}

func (s *ReviewStep) Title() string { return "Review & Generate" }
func (s *ReviewStep) Icon() string  { return "🚀" }

func (s *ReviewStep) Init() tea.Cmd {
	return nil
}

func (s *ReviewStep) Update(msg tea.Msg) (tui.Step, tea.Cmd) {
	if s.complete {
		return s, nil
	}

	if msg, ok := msg.(tea.KeyMsg); ok {
		switch msg.String() {
		case "enter":
			s.complete = true
			return s, func() tea.Msg { return tui.StepCompleteMsg{} }
		case "backspace":
			return s, func() tea.Msg { return tui.StepBackMsg{} }
		case "esc":
			return s, func() tea.Msg { return tui.StepBackMsg{} }
		}
	}
	return s, nil
}

func (s *ReviewStep) View(width int) string {
	out := s.summary.View(width) + "\n\n"
	out += "  " + s.styles.AccentTxt.Render("Press Enter to generate project, Backspace to go back") + "\n\n"
	out += s.kbd.View()
	return out
}

func (s *ReviewStep) Complete() bool {
	return s.complete
}

func (s *ReviewStep) Summary() string {
	return "confirmed"
}

func (s *ReviewStep) Apply(ctx *tui.WizardContext) {
	// No additional data to apply — scaffolding is handled by the caller.
}
