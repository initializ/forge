package steps

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/initializ/forge/forge-cli/internal/tui"
)

func newTestCompressionStep(t *testing.T) *CompressionStep {
	t.Helper()
	styles := tui.NewStyleSet(tui.DarkTheme)
	s := NewCompressionStep(styles)
	_ = s.Init()
	return s
}

// pressCompression injects a key and returns the updated step plus the
// message produced by the returned command (nil when there is no command).
func pressCompression(t *testing.T, s *CompressionStep, key tea.KeyType) (*CompressionStep, tea.Msg) {
	t.Helper()
	updated, cmd := s.Update(tea.KeyMsg{Type: key})
	step := updated.(*CompressionStep)
	if cmd == nil {
		return step, nil
	}
	return step, cmd()
}

// Regression: selecting an option MUST emit tui.StepCompleteMsg — the wizard
// advances only on that message, so a step that merely sets complete=true
// strands the user on the step (found live: TUI hung after selecting the
// compression option; Esc was the only way out).
func TestCompressionStep_EnterEmitsStepComplete(t *testing.T) {
	s := newTestCompressionStep(t)

	s, msg := pressCompression(t, s, tea.KeyEnter) // select "Enabled" (first item)
	if _, ok := msg.(tui.StepCompleteMsg); !ok {
		t.Fatalf("enter must emit tui.StepCompleteMsg, got %T", msg)
	}
	if !s.Complete() {
		t.Fatal("step should be complete after selection")
	}

	ctx := tui.NewWizardContext()
	s.Apply(ctx)
	if !ctx.Compression {
		t.Fatal("selecting Enabled should set ctx.Compression")
	}
}

func TestCompressionStep_DisabledSelection(t *testing.T) {
	s := newTestCompressionStep(t)

	s, _ = pressCompression(t, s, tea.KeyDown) // move to "Disabled"
	s, msg := pressCompression(t, s, tea.KeyEnter)
	if _, ok := msg.(tui.StepCompleteMsg); !ok {
		t.Fatalf("enter must emit tui.StepCompleteMsg, got %T", msg)
	}

	ctx := tui.NewWizardContext()
	s.Apply(ctx)
	if ctx.Compression {
		t.Fatal("selecting Disabled should leave ctx.Compression false")
	}
}

// Regression: navigating BACK to the step re-runs Init, which must reset the
// selection state — otherwise the completed selector swallows all input and
// the wizard is stuck here on revisit.
func TestCompressionStep_BackNavigationResets(t *testing.T) {
	s := newTestCompressionStep(t)
	s, _ = pressCompression(t, s, tea.KeyEnter)
	if !s.Complete() {
		t.Fatal("precondition: step complete")
	}

	_ = s.Init() // wizard re-Inits the step on StepBackMsg

	if s.Complete() {
		t.Fatal("Init must reset completion for back-navigation")
	}
	// The step must accept a fresh selection after the reset.
	_, msg := pressCompression(t, s, tea.KeyEnter)
	if _, ok := msg.(tui.StepCompleteMsg); !ok {
		t.Fatalf("step did not accept a new selection after reset, got %T", msg)
	}
}
