package components

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// EgressDomain represents a domain with its source annotation.
type EgressDomain struct {
	Domain string
	Source string // e.g., "model provider", "channel", "tool", "skill"
}

// EgressDisplay shows a read-only list of egress domains.
type EgressDisplay struct {
	Domains []EgressDomain
	done    bool

	// Styles
	PrimaryStyle   lipgloss.Style
	DimStyle       lipgloss.Style
	BorderStyle    lipgloss.Style
	AccentStyle    lipgloss.Style
	SecondaryStyle lipgloss.Style
	kbd            KbdHint
}

// NewEgressDisplay creates a new egress domain display.
func NewEgressDisplay(domains []EgressDomain, primaryStyle, dimStyle, borderStyle, accentStyle, secondaryStyle lipgloss.Style, kbdKeyStyle, kbdDescStyle lipgloss.Style) EgressDisplay {
	kbd := NewKbdHint(kbdKeyStyle, kbdDescStyle)
	kbd.Bindings = []KeyBinding{
		{Key: "⏎", Desc: "accept"},
		{Key: "backspace", Desc: "back"},
		{Key: "esc", Desc: "quit"},
	}

	return EgressDisplay{
		Domains:        domains,
		PrimaryStyle:   primaryStyle,
		DimStyle:       dimStyle,
		BorderStyle:    borderStyle,
		AccentStyle:    accentStyle,
		SecondaryStyle: secondaryStyle,
		kbd:            kbd,
	}
}

// Init resets done state so the component can be re-used after back-navigation.
func (e *EgressDisplay) Init() tea.Cmd {
	e.done = false
	return nil
}

// Update handles keyboard input.
func (e EgressDisplay) Update(msg tea.Msg) (EgressDisplay, tea.Cmd) {
	if e.done {
		return e, nil
	}

	if msg, ok := msg.(tea.KeyMsg); ok {
		switch msg.String() {
		case "enter":
			e.done = true
		}
	}

	return e, nil
}

// View renders the egress domain list.
func (e EgressDisplay) View(width int) string {
	var out string

	header := e.AccentStyle.Render(fmt.Sprintf("  Network Egress · restricted · %d domains", len(e.Domains)))
	out += header + "\n\n"

	boxWidth := width - 8
	if boxWidth < 30 {
		boxWidth = 30
	}

	var content string
	for _, d := range e.Domains {
		domain := e.PrimaryStyle.Render(d.Domain)
		source := e.DimStyle.Render(fmt.Sprintf(" ← %s", d.Source))
		content += fmt.Sprintf("  %s%s\n", domain, source)
	}

	box := e.BorderStyle.Width(boxWidth).Render(content)
	out += "  " + box + "\n"

	out += "\n" + e.kbd.View()
	return out
}

// Done returns true when the user has accepted.
func (e EgressDisplay) Done() bool {
	return e.done
}
