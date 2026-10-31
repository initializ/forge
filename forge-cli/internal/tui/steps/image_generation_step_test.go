package steps

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/initializ/forge/forge-cli/internal/tui"
)

func newTestImageGenStep(t *testing.T) *ImageGenerationStep {
	t.Helper()
	styles := tui.NewStyleSet(tui.DarkTheme)
	s := NewImageGenerationStep(styles)
	_ = s.Init()
	return s
}

func pressImageGen(t *testing.T, s *ImageGenerationStep, key tea.KeyType) (*ImageGenerationStep, tea.Msg) {
	t.Helper()
	updated, cmd := s.Update(tea.KeyMsg{Type: key})
	step := updated.(*ImageGenerationStep)
	if cmd == nil {
		return step, nil
	}
	return step, cmd()
}

// Skip hides the step for providers/models that can't generate images and
// shows it for the ones that can — the wizard relies on this in both
// directions, so navigation must step over it cleanly.
func TestImageGenerationStep_Skip(t *testing.T) {
	s := NewImageGenerationStep(tui.NewStyleSet(tui.DarkTheme))

	shown := []*tui.WizardContext{
		{Provider: "openai-responses", ModelName: "gpt-5"},
		{Provider: "gemini", ModelName: "gemini-3-pro-image"},
		{Provider: "gemini", CustomModel: "gemini-2.5-flash-image"}, // falls back to CustomModel
	}
	for _, ctx := range shown {
		if s.Skip(ctx) {
			t.Errorf("Skip(provider=%q model=%q/%q) = true, want false (should be shown)", ctx.Provider, ctx.ModelName, ctx.CustomModel)
		}
	}

	hidden := []*tui.WizardContext{
		{Provider: "gemini", ModelName: "gemini-3.8-flash"},
		{Provider: "openai", ModelName: "gpt-5"},
		{Provider: "anthropic", ModelName: "claude-opus-4-8"},
		{Provider: "", ModelName: ""},
	}
	for _, ctx := range hidden {
		if !s.Skip(ctx) {
			t.Errorf("Skip(provider=%q model=%q) = false, want true (should be hidden)", ctx.Provider, ctx.ModelName)
		}
	}
}

func TestImageGenerationStep_EnabledSelection(t *testing.T) {
	s := newTestImageGenStep(t)

	s, msg := pressImageGen(t, s, tea.KeyEnter) // "Enabled" (first item)
	if _, ok := msg.(tui.StepCompleteMsg); !ok {
		t.Fatalf("enter must emit tui.StepCompleteMsg, got %T", msg)
	}
	ctx := tui.NewWizardContext()
	s.Apply(ctx)
	if !ctx.ImageGeneration {
		t.Fatal("selecting Enabled should set ctx.ImageGeneration")
	}
}

func TestImageGenerationStep_DisabledSelection(t *testing.T) {
	s := newTestImageGenStep(t)

	s, _ = pressImageGen(t, s, tea.KeyDown) // move to "Disabled"
	s, msg := pressImageGen(t, s, tea.KeyEnter)
	if _, ok := msg.(tui.StepCompleteMsg); !ok {
		t.Fatalf("enter must emit tui.StepCompleteMsg, got %T", msg)
	}
	ctx := tui.NewWizardContext()
	s.Apply(ctx)
	if ctx.ImageGeneration {
		t.Fatal("selecting Disabled should leave ctx.ImageGeneration false")
	}
}

// Prepare resets completion and recomputes support on forward entry.
func TestImageGenerationStep_PrepareResets(t *testing.T) {
	s := newTestImageGenStep(t)
	s, _ = pressImageGen(t, s, tea.KeyEnter)
	if !s.Complete() {
		t.Fatal("precondition: step complete")
	}
	s.Prepare(&tui.WizardContext{Provider: "gemini", ModelName: "gemini-3-pro-image"})
	if s.Complete() {
		t.Fatal("Prepare must reset completion")
	}
}
