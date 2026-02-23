package components

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// MultiSelectItem represents an option in a multi-select list.
type MultiSelectItem struct {
	Label           string
	Value           string
	Description     string
	Icon            string
	RequirementLine string
	Checked         bool
}

// MultiSelect is a navigable checkbox list.
type MultiSelect struct {
	Items  []MultiSelectItem
	cursor int
	done   bool

	// Styles
	AccentColor    lipgloss.Color
	AccentDimColor lipgloss.Color
	PrimaryColor   lipgloss.Color
	SecondaryColor lipgloss.Color
	DimColor       lipgloss.Color
	ActiveBorder   lipgloss.Style
	InactiveBorder lipgloss.Style
	kbd            KbdHint
}

// NewMultiSelect creates a new multi-select component.
func NewMultiSelect(items []MultiSelectItem, accentColor, accentDimColor, primaryColor, secondaryColor, dimColor lipgloss.Color, activeBorder, inactiveBorder lipgloss.Style, kbdKeyStyle, kbdDescStyle lipgloss.Style) MultiSelect {
	kbd := NewKbdHint(kbdKeyStyle, kbdDescStyle)
	kbd.Bindings = MultiSelectHints()

	return MultiSelect{
		Items:          items,
		AccentColor:    accentColor,
		AccentDimColor: accentDimColor,
		PrimaryColor:   primaryColor,
		SecondaryColor: secondaryColor,
		DimColor:       dimColor,
		ActiveBorder:   activeBorder,
		InactiveBorder: inactiveBorder,
		kbd:            kbd,
	}
}

// Init resets done state so the component can be re-used after back-navigation.
func (m *MultiSelect) Init() tea.Cmd {
	m.done = false
	return nil
}

// Update handles keyboard input.
func (m MultiSelect) Update(msg tea.Msg) (MultiSelect, tea.Cmd) {
	if m.done {
		return m, nil
	}

	if msg, ok := msg.(tea.KeyMsg); ok {
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.Items)-1 {
				m.cursor++
			}
		case " ":
			m.Items[m.cursor].Checked = !m.Items[m.cursor].Checked
		case "enter":
			m.done = true
		}
	}

	return m, nil
}

// View renders the multi-select list.
func (m MultiSelect) View(width int) string {
	var out string

	itemWidth := width - 6
	if itemWidth < 30 {
		itemWidth = 30
	}

	for i, item := range m.Items {
		isCursor := i == m.cursor
		var checkbox, icon, label, desc string

		icon = item.Icon + "  "

		if item.Checked {
			checkbox = lipgloss.NewStyle().Foreground(m.AccentColor).Render("☑")
		} else {
			checkbox = lipgloss.NewStyle().Foreground(m.DimColor).Render("☐")
		}

		if isCursor {
			label = lipgloss.NewStyle().Foreground(m.PrimaryColor).Bold(true).Render(item.Label)
			if item.Description != "" {
				desc += "\n      " + lipgloss.NewStyle().Foreground(m.SecondaryColor).Render(item.Description)
			}
			if item.RequirementLine != "" {
				desc += "\n      " + lipgloss.NewStyle().Foreground(m.AccentDimColor).Render("⚡ "+item.RequirementLine)
			}
		} else {
			label = lipgloss.NewStyle().Foreground(m.SecondaryColor).Render(item.Label)
		}

		firstLine := fmt.Sprintf("  %s%s", icon, label)
		firstLineWidth := lipgloss.Width(firstLine)
		padding := itemWidth - firstLineWidth - 4
		if padding < 1 {
			padding = 1
		}
		content := firstLine + strings.Repeat(" ", padding) + checkbox
		if desc != "" {
			content += desc
		}

		var border lipgloss.Style
		if isCursor {
			border = m.ActiveBorder.Width(itemWidth)
		} else {
			border = m.InactiveBorder.Width(itemWidth)
		}

		out += "  " + border.Render(content) + "\n"
	}

	out += "\n" + m.kbd.View()
	return out
}

// Done returns true when selection is confirmed.
func (m MultiSelect) Done() bool {
	return m.done
}

// Reset clears the done state so the user can re-select.
func (m *MultiSelect) Reset() {
	m.done = false
}

// SelectedValues returns the values of all checked items.
func (m MultiSelect) SelectedValues() []string {
	var vals []string
	for _, item := range m.Items {
		if item.Checked {
			vals = append(vals, item.Value)
		}
	}
	return vals
}

// SelectedLabels returns the labels of all checked items.
func (m MultiSelect) SelectedLabels() []string {
	var labels []string
	for _, item := range m.Items {
		if item.Checked {
			labels = append(labels, item.Label)
		}
	}
	return labels
}
