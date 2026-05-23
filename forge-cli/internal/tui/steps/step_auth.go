package steps

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/initializ/forge/forge-cli/internal/tui"
	"github.com/initializ/forge/forge-cli/internal/tui/components"
)

// authPhase is the internal state machine of the auth step.
type authPhase int

const (
	authSelectPhase authPhase = iota
	authOIDCIssuerPhase
	authOIDCAudiencePhase
	authOIDCGroupsClaimPhase
	authHTTPURLPhase
	authHTTPOrgPhase
	authDonePhase
)

// Auth mode constants — stored in WizardContext.AuthMode and read by the
// init scaffold to decide what to render in forge.yaml.
const (
	AuthModeNone         = "none"
	AuthModeOIDC         = "oidc"
	AuthModeHTTPVerifier = "http_verifier"
	AuthModeCustom       = "custom"
)

// AuthStep is the wizard step that configures the A2A server's auth chain.
//
// UX shape:
//
//	┌─ select provider type
//	│  ○ None  (default)
//	│  ○ OIDC
//	│  ○ HTTP Verifier
//	│  ○ Custom (edit forge.yaml manually)
//	└─ on choice:
//	     OIDC          → issuer → audience → groups claim → done
//	     HTTP Verifier → url → default org → done
//	     None / Custom → done immediately
//
// Per-phase: backspace on empty input returns to the picker (Esc-like).
type AuthStep struct {
	styles *tui.StyleSet
	phase  authPhase

	selector components.SingleSelect
	input    components.TextInput

	mode string // selected provider type

	// OIDC settings
	issuer      string
	audience    string
	groupsClaim string

	// HTTP verifier settings
	httpURL string
	httpOrg string

	complete bool
}

// NewAuthStep constructs the auth step.
func NewAuthStep(styles *tui.StyleSet) *AuthStep {
	items := []components.SingleSelectItem{
		{
			Label:       "None",
			Value:       AuthModeNone,
			Description: "Anonymous access — no auth: block in forge.yaml",
			Icon:        "🔓",
		},
		{
			Label:       "OIDC (JWT)",
			Value:       AuthModeOIDC,
			Description: "Auth0, Keycloak, Azure AD, Google, Okta-OIDC, …",
			Icon:        "🔐",
		},
		{
			Label:       "HTTP Verifier",
			Value:       AuthModeHTTPVerifier,
			Description: "Legacy — POST tokens to your own /verify endpoint",
			Icon:        "🔁",
		},
		{
			Label:       "Custom",
			Value:       AuthModeCustom,
			Description: "Write a commented stub — I'll edit forge.yaml myself",
			Icon:        "✏️",
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

	return &AuthStep{
		styles:   styles,
		selector: selector,
	}
}

func (s *AuthStep) Title() string { return "Authentication" }
func (s *AuthStep) Icon() string  { return "🔐" }

func (s *AuthStep) Init() tea.Cmd { return s.selector.Init() }

func (s *AuthStep) Update(msg tea.Msg) (tui.Step, tea.Cmd) {
	if s.complete {
		return s, nil
	}

	// Forward window-size events to whichever component is active.
	if wsm, ok := msg.(tea.WindowSizeMsg); ok {
		if s.phase == authSelectPhase {
			updated, cmd := s.selector.Update(wsm)
			s.selector = updated
			return s, cmd
		}
		updated, cmd := s.input.Update(wsm)
		s.input = updated
		return s, cmd
	}

	switch s.phase {
	case authSelectPhase:
		return s.updateSelect(msg)
	case authOIDCIssuerPhase, authOIDCAudiencePhase, authOIDCGroupsClaimPhase,
		authHTTPURLPhase, authHTTPOrgPhase:
		return s.updateInput(msg)
	}
	return s, nil
}

func (s *AuthStep) updateSelect(msg tea.Msg) (tui.Step, tea.Cmd) {
	updated, cmd := s.selector.Update(msg)
	s.selector = updated
	if !s.selector.Done() {
		return s, cmd
	}

	_, val := s.selector.Selected()
	s.mode = val

	switch val {
	case AuthModeNone, AuthModeCustom:
		// No further input — finish.
		s.complete = true
		s.phase = authDonePhase
		return s, doneCmd()
	case AuthModeOIDC:
		s.phase = authOIDCIssuerPhase
		s.input = s.newTextInput(
			"Issuer URL",
			"https://login.example.com",
			validateHTTPSURL,
		)
		return s, s.input.Init()
	case AuthModeHTTPVerifier:
		s.phase = authHTTPURLPhase
		s.input = s.newTextInput(
			"Verifier URL",
			"https://auth.example.com/verify",
			validateHTTPSURL,
		)
		return s, s.input.Init()
	}
	return s, cmd
}

func (s *AuthStep) updateInput(msg tea.Msg) (tui.Step, tea.Cmd) {
	// Backspace on empty input returns to the picker.
	if km, ok := msg.(tea.KeyMsg); ok && km.String() == "backspace" {
		if s.input.Value() == "" {
			s.phase = authSelectPhase
			s.mode = ""
			s.selector.Reset()
			return s, s.selector.Init()
		}
	}

	updated, cmd := s.input.Update(msg)
	s.input = updated
	if !s.input.Done() {
		return s, cmd
	}

	v := strings.TrimSpace(s.input.Value())
	switch s.phase {
	case authOIDCIssuerPhase:
		s.issuer = strings.TrimRight(v, "/")
		s.phase = authOIDCAudiencePhase
		s.input = s.newTextInput("Audience", "api://forge", validateNonEmpty)
		return s, s.input.Init()

	case authOIDCAudiencePhase:
		s.audience = v
		s.phase = authOIDCGroupsClaimPhase
		s.input = s.newTextInput(
			"Groups claim (default: groups — press Enter to accept)",
			"groups",
			nil, // optional
		)
		return s, s.input.Init()

	case authOIDCGroupsClaimPhase:
		s.groupsClaim = v // may be ""
		s.complete = true
		s.phase = authDonePhase
		return s, doneCmd()

	case authHTTPURLPhase:
		s.httpURL = v
		s.phase = authHTTPOrgPhase
		s.input = s.newTextInput(
			"Default org_id (optional — press Enter to skip)",
			"",
			nil,
		)
		return s, s.input.Init()

	case authHTTPOrgPhase:
		s.httpOrg = v
		s.complete = true
		s.phase = authDonePhase
		return s, doneCmd()
	}

	return s, cmd
}

func (s *AuthStep) newTextInput(label, placeholder string, validate func(string) error) components.TextInput {
	return components.NewTextInput(
		label,
		placeholder,
		false, // no slug hint
		validate,
		s.styles.Theme.Accent,
		s.styles.AccentTxt,
		s.styles.InactiveBorder,
		s.styles.ErrorTxt,
		s.styles.DimTxt,
		s.styles.KbdKey,
		s.styles.KbdDesc,
	)
}

func (s *AuthStep) View(width int) string {
	switch s.phase {
	case authSelectPhase:
		return s.selector.View(width)
	case authDonePhase:
		return ""
	default:
		return s.input.View(width)
	}
}

func (s *AuthStep) Complete() bool { return s.complete }

func (s *AuthStep) Summary() string {
	switch s.mode {
	case "", AuthModeNone:
		return "Anonymous (no auth)"
	case AuthModeOIDC:
		host := hostnameOrEmpty(s.issuer)
		if host == "" {
			return "OIDC"
		}
		return "OIDC · " + host
	case AuthModeHTTPVerifier:
		host := hostnameOrEmpty(s.httpURL)
		if host == "" {
			return "HTTP Verifier"
		}
		return "HTTP Verifier · " + host
	case AuthModeCustom:
		return "Custom (edit forge.yaml)"
	}
	return s.mode
}

// Apply writes the auth selection back to the wizard context.
//
//   - AuthMode is always set (defaults to "none").
//   - AuthSettings is the typed settings block ready to copy into forge.yaml.
//   - AuthEgressHosts captures the outbound hosts the runner will need
//     reachable; the init scaffold merges these into egress.allowed_domains.
func (s *AuthStep) Apply(ctx *tui.WizardContext) {
	mode := s.mode
	if mode == "" {
		mode = AuthModeNone
	}
	ctx.AuthMode = mode

	switch mode {
	case AuthModeOIDC:
		settings := map[string]any{
			"issuer":   s.issuer,
			"audience": s.audience,
		}
		// Only emit claim_map if user customized it (default name = "groups").
		if s.groupsClaim != "" && s.groupsClaim != "groups" {
			settings["claim_map"] = map[string]any{"groups": s.groupsClaim}
		}
		ctx.AuthSettings = settings
		if host := hostnameOrEmpty(s.issuer); host != "" {
			ctx.AuthEgressHosts = appendUnique(ctx.AuthEgressHosts, host)
		}
	case AuthModeHTTPVerifier:
		settings := map[string]any{"url": s.httpURL}
		if s.httpOrg != "" {
			settings["default_org"] = s.httpOrg
		}
		ctx.AuthSettings = settings
		if host := hostnameOrEmpty(s.httpURL); host != "" {
			ctx.AuthEgressHosts = appendUnique(ctx.AuthEgressHosts, host)
		}
	default:
		// None / Custom: nothing to attach.
		ctx.AuthSettings = nil
	}
}

// --- helpers ---

// validateHTTPSURL ensures the input parses as a URL and uses http or https.
// Errors are surfaced inline by the text input.
func validateHTTPSURL(val string) error {
	if val == "" {
		return fmt.Errorf("required")
	}
	u, err := url.Parse(val)
	if err != nil {
		return fmt.Errorf("not a URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("must start with http:// or https://")
	}
	if u.Host == "" {
		return fmt.Errorf("missing host")
	}
	return nil
}

// validateNonEmpty enforces a non-empty input.
func validateNonEmpty(val string) error {
	if strings.TrimSpace(val) == "" {
		return fmt.Errorf("required")
	}
	return nil
}

// hostnameOrEmpty returns the bare host (no port) from a URL, or "" on
// parse failure.
func hostnameOrEmpty(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Hostname()
}

// appendUnique appends v to slice if not already present.
func appendUnique(slice []string, v string) []string {
	if slices.Contains(slice, v) {
		return slice
	}
	return append(slice, v)
}

// doneCmd emits the wizard "step complete" message.
func doneCmd() tea.Cmd {
	return func() tea.Msg { return tui.StepCompleteMsg{} }
}
