package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// navMockStep is a STATEFUL Step for driving the wizard in tests. It mirrors
// the real steps' contract so back-navigation bugs are visible (a stateless
// mock that never completes would only exercise index arithmetic — see the
// #264 review):
//   - Update short-circuits once complete (`if s.complete { return }`), the
//     same guard every real step's Update opens with;
//   - a space key completes the step and signals advancement;
//   - Init() resets to the initial (incomplete) state — the re-entry contract
//     the wizard relies on when it calls Init() on BACK navigation.
type navMockStep struct {
	n        string
	complete bool
}

func (s *navMockStep) Title() string { return s.n }
func (s *navMockStep) Icon() string  { return "" }
func (s *navMockStep) Init() tea.Cmd { s.complete = false; return nil }
func (s *navMockStep) Update(msg tea.Msg) (Step, tea.Cmd) {
	if s.complete {
		return s, nil // inert once done — exactly the real-step guard
	}
	if k, ok := msg.(tea.KeyMsg); ok && k.Type == tea.KeySpace {
		s.complete = true
		return s, func() tea.Msg { return StepCompleteMsg{} }
	}
	return s, nil
}
func (s *navMockStep) View(int) string      { return "" }
func (s *navMockStep) Complete() bool       { return s.complete }
func (s *navMockStep) Summary() string      { return "" }
func (s *navMockStep) Apply(*WizardContext) {}

// TestWizard_EscGoesBack pins the global back-navigation: esc returns to
// the previous step from anywhere.
func TestWizard_EscGoesBack(t *testing.T) {
	w := NewWizardModel(TermTheme{}, []Step{&navMockStep{n: "a"}, &navMockStep{n: "b"}, &navMockStep{n: "c"}}, "v")

	// Advance to the third step (index 2).
	m, _ := w.Update(StepCompleteMsg{})
	w = m.(WizardModel)
	m, _ = w.Update(StepCompleteMsg{})
	w = m.(WizardModel)
	if w.current != 2 {
		t.Fatalf("setup: current=%d want 2", w.current)
	}

	// esc -> back to step 1.
	m, _ = w.Update(tea.KeyMsg{Type: tea.KeyEsc})
	w = m.(WizardModel)
	if w.current != 1 {
		t.Errorf("esc should go back to step 1, got %d", w.current)
	}
	// esc again -> back to step 0.
	m, _ = w.Update(tea.KeyMsg{Type: tea.KeyEsc})
	w = m.(WizardModel)
	if w.current != 0 {
		t.Errorf("esc should go back to step 0, got %d", w.current)
	}
	if w.err != nil {
		t.Errorf("back-navigation must not cancel the wizard: %v", w.err)
	}
}

// TestWizard_EscAtFirstStepCancels — esc with nowhere to go back cancels.
func TestWizard_EscAtFirstStepCancels(t *testing.T) {
	w := NewWizardModel(TermTheme{}, []Step{&navMockStep{n: "a"}, &navMockStep{n: "b"}}, "v")
	m, _ := w.Update(tea.KeyMsg{Type: tea.KeyEsc})
	w = m.(WizardModel)
	if w.err == nil {
		t.Error("esc at the first step should cancel the wizard")
	}
}

// TestWizard_BackReEntersUsableStep is the #264 blocking-finding regression:
// esc-back must land on a USABLE step, not an inert completed one. It drives a
// real completion so the step's `complete` guard is live, then esc-backs into
// it and re-completes — the exact flow the feature exists for.
func TestWizard_BackReEntersUsableStep(t *testing.T) {
	stepB := &navMockStep{n: "b"}
	w := NewWizardModel(TermTheme{}, []Step{&navMockStep{n: "a"}, stepB, &navMockStep{n: "c"}}, "v")

	// Advance onto step b (index 1).
	m, _ := w.Update(StepCompleteMsg{})
	w = m.(WizardModel)
	if w.current != 1 {
		t.Fatalf("setup: current=%d want 1", w.current)
	}

	// Complete step b (space), then advance onto step c.
	m, _ = w.Update(tea.KeyMsg{Type: tea.KeySpace})
	w = m.(WizardModel)
	if !stepB.complete {
		t.Fatal("space should complete step b")
	}
	m, _ = w.Update(StepCompleteMsg{})
	w = m.(WizardModel)
	if w.current != 2 {
		t.Fatalf("should be on step c (2), got %d", w.current)
	}

	// esc back into step b. The wizard must call Init(), which resets the
	// step so it's no longer inert. THIS is the soft-lock assertion: without
	// the reset, stepB.complete stays true and every input is ignored.
	m, _ = w.Update(tea.KeyMsg{Type: tea.KeyEsc})
	w = m.(WizardModel)
	if w.current != 1 {
		t.Fatalf("esc should return to step b (1), got %d", w.current)
	}
	if stepB.complete {
		t.Fatal("SOFT-LOCK: esc-back re-entered step b without resetting complete")
	}

	// And it accepts input again — re-completes and re-advances.
	m, _ = w.Update(tea.KeyMsg{Type: tea.KeySpace})
	w = m.(WizardModel)
	if !stepB.complete {
		t.Error("step b should accept input after back-navigation")
	}
	m, _ = w.Update(StepCompleteMsg{})
	w = m.(WizardModel)
	if w.current != 2 {
		t.Errorf("re-completing step b should advance to c again, got %d", w.current)
	}
}
