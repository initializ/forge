package steps

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/initializ/forge/forge-cli/internal/tui"
	"github.com/initializ/forge/forge-cli/internal/tui/components"
)

// DeriveEgressFunc computes egress domains from wizard context.
//
// authMode + authSettings are the user's choice from the preceding Auth step.
// Auth-derived hosts (e.g. sts.<region>.amazonaws.com for aws_sigv4,
// login.microsoftonline.com for azure_ad) are merged into the egress list so
// the Egress review displays the FULL outbound surface — operators see and
// confirm the auth hosts alongside provider/channel/tool/skill hosts.
type DeriveEgressFunc func(
	provider string,
	channels, tools, skills []string,
	envVars map[string]string,
	authMode string,
	authSettings map[string]any,
) []string

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
//
// The Auth step runs BEFORE Egress in the wizard order (see init.go), so by
// the time Prepare runs, ctx.AuthMode and ctx.AuthSettings reflect the
// operator's choice and we can compute auth-derived hosts.
func (s *EgressStep) Prepare(ctx *tui.WizardContext) {
	var channels []string
	if ctx.Channel != "" && ctx.Channel != "none" {
		channels = []string{ctx.Channel}
	}

	s.domains = nil
	if s.deriveFn != nil {
		s.domains = s.deriveFn(ctx.Provider, channels, ctx.BuiltinTools, ctx.Skills, ctx.EnvVars, ctx.AuthMode, ctx.AuthSettings)
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
	// Auth provider domains — checked FIRST so an OIDC issuer host like
	// "login.example.com" is correctly attributed to "oidc auth" rather
	// than falling through to a generic "configured" label.
	if src := authProviderForDomain(domain, ctx); src != "" {
		return src
	}

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
		"api.telegram.org":      "channel",
		"slack.com":             "channel",
		"wss-primary.slack.com": "channel",
		"api.slack.com":         "channel",
		"files.slack.com":       "channel",
	}
	if src, ok := channelDomains[domain]; ok {
		return src
	}

	// Tool domains
	toolDomains := map[string]string{
		"api.tavily.com":    "web_search tool",
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

// authProviderForDomain returns a human-friendly label when `domain` is
// known to be required by the operator's chosen auth provider, or "" when
// the domain wasn't sourced from auth.
//
// The matching is intentionally narrow: we compare against the hosts each
// provider actually contributes (computed elsewhere via authEgressHostsFromSettings).
// For oidc/http_verifier, the host is dynamic (issuer/url-derived) so we
// match by exact string against what ctx.AuthSettings says the issuer
// resolves to.
func authProviderForDomain(domain string, ctx *tui.WizardContext) string {
	if ctx == nil || ctx.AuthMode == "" || ctx.AuthMode == "none" || ctx.AuthMode == "custom" {
		return ""
	}
	switch ctx.AuthMode {
	case "aws_sigv4":
		// sts.<region>.amazonaws.com is the only contributed host.
		region, _ := ctx.AuthSettings["region"].(string)
		if region != "" && domain == "sts."+region+".amazonaws.com" {
			return "aws_sigv4 auth"
		}
	case "gcp_iap":
		if domain == "www.gstatic.com" {
			return "gcp_iap auth"
		}
	case "azure_ad":
		if domain == "login.microsoftonline.com" {
			return "azure_ad auth"
		}
		if domain == "graph.microsoft.com" {
			return "azure_ad auth (graph)"
		}
	case "oidc":
		// Best-effort: match the configured issuer's host.
		issuer, _ := ctx.AuthSettings["issuer"].(string)
		if h := hostOf(issuer); h != "" && h == domain {
			return "oidc auth"
		}
		jwks, _ := ctx.AuthSettings["jwks_url"].(string)
		if h := hostOf(jwks); h != "" && h == domain {
			return "oidc auth (jwks)"
		}
	case "http_verifier":
		url, _ := ctx.AuthSettings["url"].(string)
		if h := hostOf(url); h != "" && h == domain {
			return "http_verifier auth"
		}
	}
	return ""
}

// hostOf extracts the host portion of a URL, returning "" on parse failure
// or missing host. Kept local to avoid pulling net/url into a hot iteration
// path elsewhere.
func hostOf(raw string) string {
	if raw == "" {
		return ""
	}
	// Cheap manual split; matches what `(*url.URL).Hostname()` does for the
	// well-formed inputs the wizard collects.
	const sep = "://"
	i := indexOf(raw, sep)
	if i < 0 {
		return ""
	}
	rest := raw[i+len(sep):]
	for k := 0; k < len(rest); k++ {
		if rest[k] == '/' || rest[k] == ':' {
			return rest[:k]
		}
	}
	return rest
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
