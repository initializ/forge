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
	offset   int // index of first visible item
	height   int // terminal height (0 = no constraint)
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

// maxVisibleItems returns how many items fit in the viewport.
func (s SingleSelect) maxVisibleItems() int {
	if s.height <= 0 || len(s.Items) == 0 {
		return len(s.Items)
	}
	// Each item ≈ 4 lines (border top, content, border bottom, gap).
	// Reserve ~18 lines for wizard chrome (banner, progress, kbd hints, padding).
	available := (s.height - 18) / 4
	if available < 3 {
		available = 3
	}
	if available >= len(s.Items) {
		return len(s.Items)
	}
	return available
}

// adjustOffset ensures the cursor is within the visible window.
func (s *SingleSelect) adjustOffset() {
	maxVisible := s.maxVisibleItems()
	if s.cursor < s.offset {
		s.offset = s.cursor
	}
	if s.cursor >= s.offset+maxVisible {
		s.offset = s.cursor - maxVisible + 1
	}
	if s.offset < 0 {
		s.offset = 0
	}
}

// Update handles keyboard input.
func (s SingleSelect) Update(msg tea.Msg) (SingleSelect, tea.Cmd) {
	if s.done {
		return s, nil
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.height = msg.Height
		s.adjustOffset()
		return s, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if s.cursor > 0 {
				s.cursor--
				s.adjustOffset()
			}
		case "down", "j":
			if s.cursor < len(s.Items)-1 {
				s.cursor++
				s.adjustOffset()
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
	var b strings.Builder

	itemWidth := width - 6
	if itemWidth < 30 {
		itemWidth = 30
	}

	maxVisible := s.maxVisibleItems()
	start := s.offset
	end := start + maxVisible
	if end > len(s.Items) {
		end = len(s.Items)
	}

	// Scroll indicator: items above
	if start > 0 {
		hint := fmt.Sprintf("  ▲ %d more above", start)
		b.WriteString(lipgloss.NewStyle().Foreground(s.DimColor).Render(hint) + "\n")
	}

	for i := start; i < end; i++ {
		item := s.Items[i]
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

		b.WriteString("  " + border.Render(content) + "\n")
	}

	// Scroll indicator: items below
	if end < len(s.Items) {
		hint := fmt.Sprintf("  ▼ %d more below", len(s.Items)-end)
		b.WriteString(lipgloss.NewStyle().Foreground(s.DimColor).Render(hint) + "\n")
	}

	b.WriteString("\n" + s.kbd.View())
	return b.String()
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
