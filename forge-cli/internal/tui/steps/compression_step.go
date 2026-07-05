package steps

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/initializ/forge/forge-cli/internal/tui"
	"github.com/initializ/forge/forge-cli/internal/tui/components"
)

// CompressionStep asks whether to enable reversible context compression
// (ctxzip). When enabled, bulky tool outputs are compressed before reaching
// the LLM and everything dropped stays retrievable via the context_expand
// tool — the wizard writes `compression.enabled: true` into forge.yaml.
type CompressionStep struct {
	styles   *tui.StyleSet
	selector components.SingleSelect
	enabled  bool
	complete bool
}

// NewCompressionStep creates the compression on/off step.
func NewCompressionStep(styles *tui.StyleSet) *CompressionStep {
	items := []components.SingleSelectItem{
		{
			Label: "Enabled",
			Value: "enabled",
			Icon:  "🗜️",
			Description: "Compress bulky tool outputs before they reach the LLM (typically " +
				"60-95% fewer tokens on large results). Reversible: dropped content stays " +
				"retrievable via the context_expand tool.",
		},
		{
			Label:       "Disabled",
			Value:       "disabled",
			Icon:        "📄",
			Description: "Send tool outputs to the LLM verbatim. Enable later with compression.enabled: true in forge.yaml or FORGE_COMPRESSION=true.",
		},
	}

	selector := components.NewSingleSelect(
		items,
		styles.Theme.Accent,
		styles.Theme.Primary,
		styles.Theme.Secondary,
		styles.Theme.Dim,
		styles.Theme.Border,
		styles.Theme.ActiveBorder,
		styles.Theme.ActiveBg,
		styles.KbdKey,
		styles.KbdDesc,
	)

	return &CompressionStep{styles: styles, selector: selector}
}

func (s *CompressionStep) Title() string { return "Context Compression" }
func (s *CompressionStep) Icon() string  { return "🗜️" }

func (s *CompressionStep) Init() tea.Cmd {
	// Init also runs when the user navigates BACK to this step; reset the
	// selection state (SkillsStep pattern) or the completed selector would
	// swallow all input and strand the wizard here.
	s.complete = false
	s.selector.Reset()
	return s.selector.Init()
}

func (s *CompressionStep) Update(msg tea.Msg) (tui.Step, tea.Cmd) {
	if s.complete {
		return s, nil
	}

	updated, cmd := s.selector.Update(msg)
	s.selector = updated

	if s.selector.Done() {
		_, val := s.selector.Selected()
		s.enabled = val == "enabled"
		s.complete = true
		// The wizard advances ONLY on StepCompleteMsg — setting complete
		// without emitting it leaves the wizard stuck on this step.
		return s, func() tea.Msg { return tui.StepCompleteMsg{} }
	}
	return s, cmd
}

func (s *CompressionStep) View(width int) string {
	return s.selector.View(width)
}

func (s *CompressionStep) Complete() bool { return s.complete }

func (s *CompressionStep) Summary() string {
	if s.enabled {
		return "Enabled — reversible, originals retrievable via context_expand"
	}
	return "Disabled"
}

func (s *CompressionStep) Apply(ctx *tui.WizardContext) {
	ctx.Compression = s.enabled
}
