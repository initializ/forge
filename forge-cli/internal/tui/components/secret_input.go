package components

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// SecretInputState tracks the validation state of a secret input.
type SecretInputState int

const (
	SecretInputEditing   SecretInputState = iota
	SecretInputValidated                  // validation succeeded
	SecretInputFailed                     // validation failed
)

// SecretInput is a masked text entry with validation feedback.
type SecretInput struct {
	Label     string
	input     textinput.Model
	done      bool
	state     SecretInputState
	err       string
	allowSkip bool

	// Styles
	LabelStyle   lipgloss.Style
	BorderStyle  lipgloss.Style
	SuccessStyle lipgloss.Style
	ErrorStyle   lipgloss.Style
	HintStyle    lipgloss.Style
	AccentColor  lipgloss.Color
	SuccessColor lipgloss.Color
	ErrorColor   lipgloss.Color
	BorderColor  lipgloss.Color
	kbd          KbdHint
}

// NewSecretInput creates a new masked input component.
func NewSecretInput(label string, allowSkip bool, accentColor, successColor, errorColor, borderColor lipgloss.Color, labelStyle, borderStyle, successStyle, errorStyle, hintStyle lipgloss.Style, kbdKeyStyle, kbdDescStyle lipgloss.Style) SecretInput {
	ti := textinput.New()
	ti.Placeholder = "paste key here"
	ti.EchoMode = textinput.EchoPassword
	ti.EchoCharacter = '•'
	ti.Focus()
	ti.CharLimit = 200
	ti.Cursor.Style = lipgloss.NewStyle().Foreground(accentColor)

	hints := InputHints()
	if allowSkip {
		hints = append(hints, KeyBinding{Key: "⏎", Desc: "(empty) skip"})
	}

	kbd := NewKbdHint(kbdKeyStyle, kbdDescStyle)
	kbd.Bindings = hints

	return SecretInput{
		Label:        label,
		input:        ti,
		allowSkip:    allowSkip,
		state:        SecretInputEditing,
		LabelStyle:   labelStyle,
		BorderStyle:  borderStyle,
		SuccessStyle: successStyle,
		ErrorStyle:   errorStyle,
		HintStyle:    hintStyle,
		AccentColor:  accentColor,
		SuccessColor: successColor,
		ErrorColor:   errorColor,
		BorderColor:  borderColor,
		kbd:          kbd,
	}
}

// Init focuses the input.
func (s SecretInput) Init() tea.Cmd {
	return textinput.Blink
}

// Update handles messages.
func (s SecretInput) Update(msg tea.Msg) (SecretInput, tea.Cmd) {
	if s.done {
		return s, nil
	}

	if msg, ok := msg.(tea.KeyMsg); ok {
		switch msg.String() {
		case "enter":
			val := strings.TrimSpace(s.input.Value())
			if val == "" && s.allowSkip {
				s.done = true
				s.state = SecretInputValidated
				return s, nil
			}
			if val == "" {
				s.err = "key is required"
				return s, nil
			}
			s.done = true
			s.err = ""
			return s, nil
		}
	}

	var cmd tea.Cmd
	s.input, cmd = s.input.Update(msg)
	s.err = ""
	return s, cmd
}

// View renders the secret input.
func (s SecretInput) View(width int) string {
	var out string

	out += "\n  " + s.LabelStyle.Render(s.Label) + "\n\n"

	inputWidth := width - 8
	if inputWidth < 20 {
		inputWidth = 20
	}
	s.input.Width = inputWidth

	// Determine border style based on state
	var borderStyle lipgloss.Style
	switch s.state {
	case SecretInputValidated:
		borderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(s.SuccessColor).
			Padding(0, 1)
	case SecretInputFailed:
		borderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(s.ErrorColor).
			Padding(0, 1)
	default:
		borderStyle = s.BorderStyle
	}

	inputBox := borderStyle.Width(inputWidth).Render(s.input.View())
	out += "  " + inputBox + "\n"

	// Status messages
	switch s.state {
	case SecretInputValidated:
		out += "  " + s.SuccessStyle.Render("✓ Key validated") + "\n"
	case SecretInputFailed:
		if s.err != "" {
			out += "  " + s.ErrorStyle.Render("✗ "+s.err) + "\n"
		}
	default:
		if s.err != "" {
			out += "  " + s.ErrorStyle.Render("✗ "+s.err) + "\n"
		}
	}

	out += "\n" + s.kbd.View()
	return out
}

// Done returns true when input is submitted.
func (s SecretInput) Done() bool {
	return s.done
}

// Value returns the current input value.
func (s SecretInput) Value() string {
	return strings.TrimSpace(s.input.Value())
}

// SetState updates the validation state and optional error.
func (s *SecretInput) SetState(state SecretInputState, errMsg string) {
	s.state = state
	s.err = errMsg
}
