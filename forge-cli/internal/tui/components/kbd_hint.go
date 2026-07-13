package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// KeyBinding represents a keyboard shortcut hint.
type KeyBinding struct {
	Key  string
	Desc string
}

// KbdHint renders a horizontal keyboard shortcut hint bar.
type KbdHint struct {
	Bindings  []KeyBinding
	KeyStyle  lipgloss.Style
	DescStyle lipgloss.Style
}

// NewKbdHint creates a KbdHint with the given styles.
func NewKbdHint(keyStyle, descStyle lipgloss.Style) KbdHint {
	return KbdHint{
		KeyStyle:  keyStyle,
		DescStyle: descStyle,
	}
}

// View renders the keyboard hints.
func (k KbdHint) View() string {
	var parts []string
	for _, b := range k.Bindings {
		part := k.KeyStyle.Render(b.Key) + " " + k.DescStyle.Render(b.Desc)
		parts = append(parts, part)
	}
	return "  " + strings.Join(parts, "    ")
}

// SelectHints returns standard hints for single-select components.
func SelectHints() []KeyBinding {
	return []KeyBinding{
		{Key: "↑↓", Desc: "navigate"},
		{Key: "⏎", Desc: "select"},
		{Key: "esc", Desc: "back"},
	}
}

// MultiSelectHints returns standard hints for multi-select components.
func MultiSelectHints() []KeyBinding {
	return []KeyBinding{
		{Key: "↑↓", Desc: "navigate"},
		{Key: "space", Desc: "toggle"},
		{Key: "⏎", Desc: "confirm"},
		{Key: "esc", Desc: "back"},
	}
}

// InputHints returns standard hints for text input components.
func InputHints() []KeyBinding {
	return []KeyBinding{
		{Key: "⏎", Desc: "submit"},
		{Key: "esc", Desc: "back"},
	}
}

// ReviewHints returns standard hints for the review step. Only `esc` is
// advertised for back: wizard-level esc supersedes the step-local backspace
// (StepBackMsg) path, so showing both would imply two behaviors where there's
// one. See #264 review.
func ReviewHints() []KeyBinding {
	return []KeyBinding{
		{Key: "⏎", Desc: "confirm"},
		{Key: "esc", Desc: "back"},
	}
}
