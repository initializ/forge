package components

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// SingleSelectItem represents an option in a single-select list.
type SingleSelectItem struct {
	Label       string
	Value       string
	Description string
	Icon        string
}

// SingleSelect is a navigable radio-button list.
type SingleSelect struct {
	Items    []SingleSelectItem
	cursor   int
	selected int
	done     bool

	// Styles
	ActiveBorder   lipgloss.Style
	InactiveBorder lipgloss.Style
	ActiveBg       lipgloss.Color
	AccentColor    lipgloss.Color
	PrimaryColor   lipgloss.Color
	SecondaryColor lipgloss.Color
	DimColor       lipgloss.Color
	kbd            KbdHint
}

// NewSingleSelect creates a new single-select component.
func NewSingleSelect(items []SingleSelectItem, accentColor, primaryColor, secondaryColor, dimColor lipgloss.Color, borderColor, activeBorderColor lipgloss.Color, activeBg lipgloss.Color, kbdKeyStyle, kbdDescStyle lipgloss.Style) SingleSelect {
	kbd := NewKbdHint(kbdKeyStyle, kbdDescStyle)
	kbd.Bindings = SelectHints()

	return SingleSelect{
		Items:          items,
		selected:       -1,
		AccentColor:    accentColor,
		PrimaryColor:   primaryColor,
		SecondaryColor: secondaryColor,
		DimColor:       dimColor,
		ActiveBg:       activeBg,
		ActiveBorder: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(activeBorderColor).
			Padding(0, 1),
		InactiveBorder: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(borderColor).
			Padding(0, 1),
		kbd: kbd,
	}
}

// Init resets done state so the component can be re-used after back-navigation.
func (s *SingleSelect) Init() tea.Cmd {
	s.done = false
	return nil
}

// Update handles keyboard input.
func (s SingleSelect) Update(msg tea.Msg) (SingleSelect, tea.Cmd) {
	if s.done {
		return s, nil
	}

	if msg, ok := msg.(tea.KeyMsg); ok {
		switch msg.String() {
		case "up", "k":
			if s.cursor > 0 {
				s.cursor--
			}
		case "down", "j":
			if s.cursor < len(s.Items)-1 {
				s.cursor++
			}
		case "enter":
			s.selected = s.cursor
			s.done = true
		}
	}

	return s, nil
}

// View renders the select list.
func (s SingleSelect) View(width int) string {
	var out string

	itemWidth := width - 6
	if itemWidth < 30 {
		itemWidth = 30
	}

	for i, item := range s.Items {
		isCursor := i == s.cursor
		var radio, icon, label, desc string

		icon = item.Icon + "  "
		if isCursor {
			radio = lipgloss.NewStyle().Foreground(s.AccentColor).Render("◉")
			label = lipgloss.NewStyle().Foreground(s.PrimaryColor).Bold(true).Render(item.Label)
			if item.Description != "" {
				desc = "\n      " + lipgloss.NewStyle().Foreground(s.SecondaryColor).Render(item.Description)
			}
		} else {
			radio = lipgloss.NewStyle().Foreground(s.DimColor).Render("○")
			label = lipgloss.NewStyle().Foreground(s.SecondaryColor).Render(item.Label)
		}

		firstLine := fmt.Sprintf("  %s%s", icon, label)
		firstLineWidth := lipgloss.Width(firstLine)
		padding := itemWidth - firstLineWidth - 4
		if padding < 1 {
			padding = 1
		}
		content := firstLine + strings.Repeat(" ", padding) + radio
		if desc != "" {
			content += desc
		}

		var border lipgloss.Style
		if isCursor {
			border = s.ActiveBorder.Width(itemWidth)
		} else {
			border = s.InactiveBorder.Width(itemWidth)
		}

		out += "  " + border.Render(content) + "\n"
	}

	out += "\n" + s.kbd.View()
	return out
}

// Done returns true when a selection has been made.
func (s SingleSelect) Done() bool {
	return s.done
}

// Reset clears the selection so the user can pick again.
func (s *SingleSelect) Reset() {
	s.done = false
	s.selected = -1
}

// Selected returns the index and value of the selected item.
func (s SingleSelect) Selected() (int, string) {
	if s.selected >= 0 && s.selected < len(s.Items) {
		return s.selected, s.Items[s.selected].Value
	}
	return -1, ""
}

// SelectedItem returns the selected item, or nil if none selected.
func (s SingleSelect) SelectedItem() *SingleSelectItem {
	if s.selected >= 0 && s.selected < len(s.Items) {
		return &s.Items[s.selected]
	}
	return nil
}
