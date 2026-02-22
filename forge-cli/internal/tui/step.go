package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Step is the interface that all wizard steps must implement.
type Step interface {
	// Title returns the step's display title.
	Title() string
	// Icon returns the step's icon/emoji.
	Icon() string
	// Init returns the initial command for this step.
	Init() tea.Cmd
	// Update handles messages and returns the updated step and command.
	Update(msg tea.Msg) (Step, tea.Cmd)
	// View renders the step content.
	View(width int) string
	// Complete returns true when the step has finished.
	Complete() bool
	// Summary returns a one-line summary for the collapsed view.
	Summary() string
	// Apply writes collected data to the wizard context.
	Apply(ctx *WizardContext)
}

// RenderProgress renders the step progress sidebar showing completed and active steps.
func RenderProgress(steps []Step, current int, styles *StyleSet, width int) string {
	var out string

	for i := 0; i < current; i++ {
		badge := styles.StepBadgeComplete.Render(" ✓ ")
		title := styles.PrimaryTxt.Bold(true).Render(steps[i].Title())
		out += fmt.Sprintf("  %s  %s\n", badge, title)
		summary := styles.SecondaryTxt.Render(steps[i].Summary())
		out += fmt.Sprintf("       %s\n\n", summary)
	}

	if current < len(steps) {
		numStr := fmt.Sprintf(" %d ", current+1)
		badge := styles.StepBadgeActive.Render(numStr)
		title := styles.PrimaryTxt.Bold(true).Render(steps[current].Title())
		dividerLen := width - 10 - lipgloss.Width(numStr) - lipgloss.Width(steps[current].Title())
		if dividerLen < 2 {
			dividerLen = 2
		}
		divider := styles.DimTxt.Render(" " + strings.Repeat("─", dividerLen))
		out += fmt.Sprintf("  %s  %s%s\n", badge, title, divider)
	}

	return out
}
