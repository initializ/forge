package steps

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/initializ/forge/forge-cli/internal/tui"
	"github.com/initializ/forge/forge-cli/internal/tui/components"
)

// DeriveEgressFunc computes egress domains from wizard context.
type DeriveEgressFunc func(provider string, channels, tools, skills []string) []string

// EgressStep handles egress domain review.
type EgressStep struct {
	styles   *tui.StyleSet
	display  components.EgressDisplay
	complete bool
	domains  []string
	deriveFn DeriveEgressFunc
	empty    bool
	prepared bool
}

// NewEgressStep creates a new egress review step.
func NewEgressStep(styles *tui.StyleSet, deriveFn DeriveEgressFunc) *EgressStep {
	return &EgressStep{
		styles:   styles,
		deriveFn: deriveFn,
	}
}

// Prepare computes egress domains using the accumulated wizard context.
func (s *EgressStep) Prepare(ctx *tui.WizardContext) {
	var channels []string
	if ctx.Channel != "" && ctx.Channel != "none" {
		channels = []string{ctx.Channel}
	}

	s.domains = nil
	if s.deriveFn != nil {
		s.domains = s.deriveFn(ctx.Provider, channels, ctx.BuiltinTools, ctx.Skills)
	}

	s.empty = len(s.domains) == 0
	s.prepared = true

	if !s.empty {
		var egressDomains []components.EgressDomain
		for _, d := range s.domains {
			source := inferSource(d, ctx)
			egressDomains = append(egressDomains, components.EgressDomain{
				Domain: d,
				Source: source,
			})
		}

		s.display = components.NewEgressDisplay(
			egressDomains,
			s.styles.PrimaryTxt,
			s.styles.DimTxt,
			s.styles.BorderedBox,
			s.styles.AccentTxt,
			s.styles.SecondaryTxt,
			s.styles.KbdKey,
			s.styles.KbdDesc,
		)
	}
}

func (s *EgressStep) Title() string { return "Egress Review" }
func (s *EgressStep) Icon() string  { return "🌐" }

func (s *EgressStep) Init() tea.Cmd {
	s.complete = false
	if s.empty {
		s.complete = true
		return func() tea.Msg { return tui.StepCompleteMsg{} }
	}
	return s.display.Init()
}

func (s *EgressStep) Update(msg tea.Msg) (tui.Step, tea.Cmd) {
	if s.complete {
		return s, nil
	}

	// Handle backspace for going back
	if msg, ok := msg.(tea.KeyMsg); ok && msg.String() == "backspace" {
		return s, func() tea.Msg { return tui.StepBackMsg{} }
	}

	updated, cmd := s.display.Update(msg)
	s.display = updated

	if s.display.Done() {
		s.complete = true
		return s, func() tea.Msg { return tui.StepCompleteMsg{} }
	}

	return s, cmd
}

func (s *EgressStep) View(width int) string {
	if s.empty {
		return fmt.Sprintf("  %s\n", s.styles.DimTxt.Render("No egress domains needed."))
	}
	return s.display.View(width)
}

func (s *EgressStep) Complete() bool {
	return s.complete
}

func (s *EgressStep) Summary() string {
	if len(s.domains) == 0 {
		return "none"
	}
	return fmt.Sprintf("restricted · %d domains", len(s.domains))
}

func (s *EgressStep) Apply(ctx *tui.WizardContext) {
	ctx.EgressDomains = s.domains
}

// inferSource guesses the source of an egress domain based on context.
func inferSource(domain string, ctx *tui.WizardContext) string {
	// Provider domains
	providerDomains := map[string]string{
		"api.openai.com":                    "model provider",
		"api.anthropic.com":                 "model provider",
		"generativelanguage.googleapis.com": "model provider",
	}
	if src, ok := providerDomains[domain]; ok {
		return src
	}

	// Channel domains
	channelDomains := map[string]string{
		"api.telegram.org": "channel",
		"slack.com":        "channel",
		"hooks.slack.com":  "channel",
		"api.slack.com":    "channel",
	}
	if src, ok := channelDomains[domain]; ok {
		return src
	}

	// Tool domains
	toolDomains := map[string]string{
		"api.perplexity.ai": "web_search tool",
	}
	if src, ok := toolDomains[domain]; ok {
		return src
	}

	// Skill domains
	skillDomains := map[string]string{
		"api.github.com":         "github skill",
		"github.com":             "github skill",
		"api.openweathermap.org": "weather skill",
		"api.weatherapi.com":     "weather skill",
	}
	if src, ok := skillDomains[domain]; ok {
		return src
	}

	return "configured"
}
