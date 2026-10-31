package steps

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/initializ/forge/forge-cli/internal/tui"
	"github.com/initializ/forge/forge-cli/internal/tui/components"
	"github.com/initializ/forge/forge-core/runtime"
)

// ImageGenerationStep asks whether the agent's model should generate image
// output (#255). It is shown ONLY for providers/models that can actually
// generate images — openai-responses, or a Gemini image model — determined by
// runtime.ModelSupportsImageGen from the provider + model chosen in the earlier
// provider step. For every other provider the step skips itself (Skip), so it
// never appears where the flag would be a no-op. When enabled the wizard writes
// `model.image_generation: true` into forge.yaml.
type ImageGenerationStep struct {
	styles    *tui.StyleSet
	selector  components.SingleSelect
	supported bool
	enabled   bool
	complete  bool
}

// NewImageGenerationStep creates the image-generation on/off step.
func NewImageGenerationStep(styles *tui.StyleSet) *ImageGenerationStep {
	return &ImageGenerationStep{styles: styles}
}

// Prepare computes whether the step applies to the provider + model selected in
// the provider step. Runs on forward advance, before Init.
func (s *ImageGenerationStep) Prepare(ctx *tui.WizardContext) {
	model := ctx.ModelName
	if model == "" {
		model = ctx.CustomModel
	}
	s.supported = runtime.ModelSupportsImageGen(ctx.Provider, model)
	s.enabled = false
	s.complete = false
}

// Skip hides the step for providers/models that can't generate images. Checked
// by the wizard in both directions so navigation steps cleanly over it.
func (s *ImageGenerationStep) Skip(ctx *tui.WizardContext) bool {
	model := ctx.ModelName
	if model == "" {
		model = ctx.CustomModel
	}
	return !runtime.ModelSupportsImageGen(ctx.Provider, model)
}

func (s *ImageGenerationStep) Title() string { return "Image Generation" }
func (s *ImageGenerationStep) Icon() string  { return "🎨" }

func (s *ImageGenerationStep) Init() tea.Cmd {
	// Re-entry contract (CompressionStep pattern): Init also runs on BACK
	// navigation (Prepare does not), so rebuild the selector and reset state
	// or a completed selector strands the wizard here.
	s.complete = false

	items := []components.SingleSelectItem{
		{
			Label:       "Enabled",
			Value:       "enabled",
			Icon:        "🎨",
			Description: "Let the model return generated images as part of its response. Written as model.image_generation: true.",
		},
		{
			Label:       "Disabled",
			Value:       "disabled",
			Icon:        "💬",
			Description: "Text-only responses. Enable later with model.image_generation: true in forge.yaml.",
		},
	}

	s.selector = components.NewSingleSelect(
		items,
		s.styles.Theme.Accent,
		s.styles.Theme.Primary,
		s.styles.Theme.Secondary,
		s.styles.Theme.Dim,
		s.styles.Theme.Border,
		s.styles.Theme.ActiveBorder,
		s.styles.Theme.ActiveBg,
		s.styles.KbdKey,
		s.styles.KbdDesc,
	)
	return s.selector.Init()
}

func (s *ImageGenerationStep) Update(msg tea.Msg) (tui.Step, tea.Cmd) {
	if s.complete {
		return s, nil
	}

	updated, cmd := s.selector.Update(msg)
	s.selector = updated

	if s.selector.Done() {
		_, val := s.selector.Selected()
		s.enabled = val == "enabled"
		s.complete = true
		return s, func() tea.Msg { return tui.StepCompleteMsg{} }
	}
	return s, cmd
}

func (s *ImageGenerationStep) View(width int) string {
	return s.selector.View(width)
}

func (s *ImageGenerationStep) Complete() bool { return s.complete }

func (s *ImageGenerationStep) Summary() string {
	if s.enabled {
		return "Enabled — model may return generated images"
	}
	return "Disabled"
}

func (s *ImageGenerationStep) Apply(ctx *tui.WizardContext) {
	ctx.ImageGeneration = s.enabled
}
