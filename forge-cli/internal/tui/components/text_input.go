package components

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// TextInput is a styled text entry component wrapping bubbles/textinput.
type TextInput struct {
	Label      string
	input      textinput.Model
	done       bool
	err        string
	slugHint   bool // show slug hint below input
	validateFn func(string) error

	// Styles
	LabelStyle  lipgloss.Style
	BorderStyle lipgloss.Style
	ErrorStyle  lipgloss.Style
	HintStyle   lipgloss.Style
	AccentColor lipgloss.Color
	kbd         KbdHint
}

// NewTextInput creates a new styled text input.
func NewTextInput(label, placeholder string, slugHint bool, validateFn func(string) error, accentColor lipgloss.Color, labelStyle, borderStyle, errorStyle, hintStyle lipgloss.Style, kbdKeyStyle, kbdDescStyle lipgloss.Style) TextInput {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.Focus()
	ti.CharLimit = 100
	ti.Cursor.Style = lipgloss.NewStyle().Foreground(accentColor)

	kbd := NewKbdHint(kbdKeyStyle, kbdDescStyle)
	kbd.Bindings = InputHints()

	return TextInput{
		Label:       label,
		input:       ti,
		slugHint:    slugHint,
		validateFn:  validateFn,
		LabelStyle:  labelStyle,
		BorderStyle: borderStyle,
		ErrorStyle:  errorStyle,
		HintStyle:   hintStyle,
		AccentColor: accentColor,
		kbd:         kbd,
	}
}

// Init focuses the text input.
func (t TextInput) Init() tea.Cmd {
	return textinput.Blink
}

// Update handles messages.
func (t TextInput) Update(msg tea.Msg) (TextInput, tea.Cmd) {
	if t.done {
		return t, nil
	}

	if msg, ok := msg.(tea.KeyMsg); ok {
		switch msg.String() {
		case "enter":
			val := strings.TrimSpace(t.input.Value())
			if t.validateFn != nil {
				if err := t.validateFn(val); err != nil {
					t.err = err.Error()
					return t, nil
				}
			}
			t.done = true
			t.err = ""
			return t, nil
		}
	}

	var cmd tea.Cmd
	t.input, cmd = t.input.Update(msg)
	t.err = "" // clear error on typing
	return t, cmd
}

// View renders the text input.
func (t TextInput) View(width int) string {
	var out string

	out += "\n  " + t.LabelStyle.Render(t.Label) + "\n\n"

	inputWidth := width - 8
	if inputWidth < 20 {
		inputWidth = 20
	}
	t.input.Width = inputWidth

	inputBox := t.BorderStyle.Width(inputWidth).Render(t.input.View())
	out += "  " + inputBox + "\n"

	if t.err != "" {
		out += "  " + t.ErrorStyle.Render("✗ "+t.err) + "\n"
	}

	if t.slugHint && t.input.Value() != "" {
		slug := slugify(t.input.Value())
		out += "  " + t.HintStyle.Render(fmt.Sprintf("→ ./%s/", slug)) + "\n"
	}

	out += "\n" + t.kbd.View()
	return out
}

// Done returns true when input is submitted.
func (t TextInput) Done() bool {
	return t.done
}

// Value returns the current input value.
func (t TextInput) Value() string {
	return strings.TrimSpace(t.input.Value())
}

// SetValue sets the input value.
func (t *TextInput) SetValue(v string) {
	t.input.SetValue(v)
}

// slugify converts a string to a URL-friendly slug.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		if r == ' ' || r == '_' {
			return '-'
		}
		return -1
	}, s)
	// Collapse multiple dashes
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
}
