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
	offset int // index of first visible item
	height int // terminal height (0 = no constraint)
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

// maxVisibleItems returns how many items fit in the viewport.
func (m MultiSelect) maxVisibleItems() int {
	if m.height <= 0 || len(m.Items) == 0 {
		return len(m.Items)
	}
	// Each item ≈ 4 lines (border top, content, border bottom, gap).
	// Reserve ~18 lines for wizard chrome (banner, progress, kbd hints, padding).
	available := (m.height - 18) / 4
	if available < 3 {
		available = 3
	}
	if available >= len(m.Items) {
		return len(m.Items)
	}
	return available
}

// adjustOffset ensures the cursor is within the visible window.
func (m *MultiSelect) adjustOffset() {
	maxVisible := m.maxVisibleItems()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+maxVisible {
		m.offset = m.cursor - maxVisible + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

// Update handles keyboard input.
func (m MultiSelect) Update(msg tea.Msg) (MultiSelect, tea.Cmd) {
	if m.done {
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.height = msg.Height
		m.adjustOffset()
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
				m.adjustOffset()
			}
		case "down", "j":
			if m.cursor < len(m.Items)-1 {
				m.cursor++
				m.adjustOffset()
			}
		case " ":
			m.Items[m.cursor].Checked = !m.Items[m.cursor].Checked
		case "enter":
			// If nothing is checked, auto-check the cursor item so the user
			// doesn't have to remember Space+Enter for a single selection.
			anyChecked := false
			for _, item := range m.Items {
				if item.Checked {
					anyChecked = true
					break
				}
			}
			if !anyChecked && len(m.Items) > 0 {
				m.Items[m.cursor].Checked = true
			}
			m.done = true
		}
	}

	return m, nil
}

// View renders the multi-select list.
func (m MultiSelect) View(width int) string {
	var b strings.Builder

	itemWidth := width - 6
	if itemWidth < 30 {
		itemWidth = 30
	}

	maxVisible := m.maxVisibleItems()
	start := m.offset
	end := start + maxVisible
	if end > len(m.Items) {
		end = len(m.Items)
	}

	// Scroll indicator: items above
	if start > 0 {
		hint := fmt.Sprintf("  ▲ %d more above", start)
		b.WriteString(lipgloss.NewStyle().Foreground(m.DimColor).Render(hint) + "\n")
	}

	for i := start; i < end; i++ {
		item := m.Items[i]
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

		b.WriteString("  " + border.Render(content) + "\n")
	}

	// Scroll indicator: items below
	if end < len(m.Items) {
		hint := fmt.Sprintf("  ▼ %d more below", len(m.Items)-end)
		b.WriteString(lipgloss.NewStyle().Foreground(m.DimColor).Render(hint) + "\n")
	}

	b.WriteString("\n" + m.kbd.View())
	return b.String()
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
