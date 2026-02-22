package components

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

// SummaryRow represents a key-value pair in the summary.
type SummaryRow struct {
	Key   string
	Value string
}

// SummaryBox renders a 2-column key/value grid in a bordered box.
type SummaryBox struct {
	Rows []SummaryRow

	// Styles
	KeyStyle    lipgloss.Style
	ValueStyle  lipgloss.Style
	BorderStyle lipgloss.Style
}

// NewSummaryBox creates a new summary box.
func NewSummaryBox(rows []SummaryRow, keyStyle, valueStyle, borderStyle lipgloss.Style) SummaryBox {
	return SummaryBox{
		Rows:        rows,
		KeyStyle:    keyStyle,
		ValueStyle:  valueStyle,
		BorderStyle: borderStyle,
	}
}

// View renders the summary box.
func (s SummaryBox) View(width int) string {
	boxWidth := width - 8
	if boxWidth < 30 {
		boxWidth = 30
	}

	var content string
	for _, row := range s.Rows {
		key := s.KeyStyle.Width(16).Render(row.Key)
		value := s.ValueStyle.Render(row.Value)
		content += fmt.Sprintf("  %s  %s\n", key, value)
	}

	return "  " + s.BorderStyle.Width(boxWidth).Render(content)
}
