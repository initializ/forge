package steps

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/initializ/forge/forge-cli/internal/tui"
	"github.com/initializ/forge/forge-cli/internal/tui/components"

	"github.com/initializ/forge/forge-core/catalog"
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
	// Phase 2: aws_sigv4
	authAWSRegionPhase
	authAWSAudiencePhase
	authAWSAccountsPhase
	// Phase 2: gcp_iap
	authGCPIAPAudiencePhase
	// Phase 2: azure_ad (single-tenant only; multi-tenant requires YAML
	// edit so it's a deliberate choice rather than an accidental toggle)
	authAADTenantPhase
	authAADAudiencePhase
	authDonePhase
)

// Auth mode constants — stored in WizardContext.AuthMode and read by the
// init scaffold to decide what to render in forge.yaml.
const (
	AuthModeNone         = "none"
	AuthModeOIDC         = "oidc"
	AuthModeHTTPVerifier = "http_verifier"
	AuthModeAWSSigv4     = "aws_sigv4"
	AuthModeGCPIAP       = "gcp_iap"
	AuthModeAzureAD      = "azure_ad"
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

	// aws_sigv4 settings
	awsRegion   string
	awsAudience string
	awsAccounts []string // 12-digit AWS account IDs

	// gcp_iap settings
	gcpAudience string

	// azure_ad settings (TUI is single-tenant only; multi-tenant
	// requires editing forge.yaml directly so it stays a deliberate
	// security decision)
	aadTenant   string
	aadAudience string

	complete bool
}

// authSelectItems projects the catalog auth modes into TUI select items.
func authSelectItems() []components.SingleSelectItem {
	ms := catalog.AllAuthModes()
	items := make([]components.SingleSelectItem, 0, len(ms))
	for _, m := range ms {
		items = append(items, components.SingleSelectItem{
			Label: m.Label, Value: m.ID, Description: m.Description, Icon: m.Icon,
		})
	}
	return items
}

// NewAuthStep constructs the auth step.
func NewAuthStep(styles *tui.StyleSet) *AuthStep {
	items := authSelectItems()

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
		authHTTPURLPhase, authHTTPOrgPhase,
		authAWSRegionPhase, authAWSAudiencePhase, authAWSAccountsPhase,
		authGCPIAPAudiencePhase,
		authAADTenantPhase, authAADAudiencePhase:
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
	case AuthModeAWSSigv4:
		s.phase = authAWSRegionPhase
		s.input = s.newTextInput(
			"AWS region",
			"us-east-1",
			validateAWSRegion,
		)
		return s, s.input.Init()
	case AuthModeGCPIAP:
		s.phase = authGCPIAPAudiencePhase
		s.input = s.newTextInput(
			"IAP audience (backend service ID from GCP console)",
			"/projects/PNUM/global/backendServices/BACKEND_ID",
			validateNonEmpty,
		)
		return s, s.input.Init()
	case AuthModeAzureAD:
		s.phase = authAADTenantPhase
		s.input = s.newTextInput(
			"Entra tenant ID (GUID)",
			"00000000-0000-0000-0000-000000000000",
			validateNonEmpty,
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

	// --- aws_sigv4 ---
	case authAWSRegionPhase:
		s.awsRegion = v
		s.phase = authAWSAudiencePhase
		s.input = s.newTextInput(
			"Audience (informational; press Enter to skip)",
			"api://forge",
			nil,
		)
		return s, s.input.Init()

	case authAWSAudiencePhase:
		s.awsAudience = v
		s.phase = authAWSAccountsPhase
		s.input = s.newTextInput(
			"Allowed AWS accounts (comma-separated 12-digit IDs; Enter to skip)",
			"412664885516,109887654321",
			validateAccountList,
		)
		return s, s.input.Init()

	case authAWSAccountsPhase:
		s.awsAccounts = parseAccountList(v)
		s.complete = true
		s.phase = authDonePhase
		return s, doneCmd()

	// --- gcp_iap ---
	case authGCPIAPAudiencePhase:
		s.gcpAudience = v
		s.complete = true
		s.phase = authDonePhase
		return s, doneCmd()

	// --- azure_ad (single-tenant) ---
	case authAADTenantPhase:
		s.aadTenant = v
		s.phase = authAADAudiencePhase
		s.input = s.newTextInput(
			"Audience (Application ID URI)",
			"api://forge",
			validateNonEmpty,
		)
		return s, s.input.Init()

	case authAADAudiencePhase:
		s.aadAudience = v
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
		// Inline the "Backspace at empty: back" hint with the input
		// so the escape hatch is discoverable from within the sub-step
		// (review #11e — the documented behavior was undiscoverable
		// because no kbd hint surfaced it).
		hint := "  " +
			s.styles.KbdKey.Render("Backspace at empty") + " " +
			s.styles.KbdDesc.Render("back to picker")
		return s.input.View(width) + "\n" + hint
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
	case AuthModeAWSSigv4:
		if len(s.awsAccounts) > 0 {
			return fmt.Sprintf("AWS Sigv4 · %s · %d account(s)", s.awsRegion, len(s.awsAccounts))
		}
		return "AWS Sigv4 · " + s.awsRegion
	case AuthModeGCPIAP:
		return "GCP IAP"
	case AuthModeAzureAD:
		return "Azure AD · single-tenant"
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
	case AuthModeAWSSigv4:
		settings := map[string]any{"region": s.awsRegion}
		if s.awsAudience != "" {
			settings["audience"] = s.awsAudience
		}
		if len(s.awsAccounts) > 0 {
			settings["allowed_accounts"] = s.awsAccounts
		}
		ctx.AuthSettings = settings
		ctx.AuthEgressHosts = appendUnique(ctx.AuthEgressHosts, "sts."+s.awsRegion+".amazonaws.com")
	case AuthModeGCPIAP:
		ctx.AuthSettings = map[string]any{"audience": s.gcpAudience}
		ctx.AuthEgressHosts = appendUnique(ctx.AuthEgressHosts, "www.gstatic.com")
	case AuthModeAzureAD:
		ctx.AuthSettings = map[string]any{
			"tenant_id": s.aadTenant,
			"audience":  s.aadAudience,
		}
		ctx.AuthEgressHosts = appendUnique(ctx.AuthEgressHosts, "login.microsoftonline.com")
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

// validateAWSRegion checks for the canonical AWS region shape
// (xx-direction-N). Doesn't enumerate regions because AWS adds them
// often; STS at startup fails fast if the host doesn't resolve.
func validateAWSRegion(val string) error {
	v := strings.TrimSpace(val)
	if v == "" {
		return fmt.Errorf("required")
	}
	// xx-direction-N, e.g. us-east-1, eu-west-2, ap-southeast-1
	parts := strings.Split(v, "-")
	if len(parts) < 3 {
		return fmt.Errorf("expected AWS region shape like 'us-east-1'")
	}
	return nil
}

// validateAccountList accepts a comma-separated list of 12-digit AWS
// account IDs. Empty input is OK (the field is optional). Each entry
// is validated; one bad entry blocks the whole field.
func validateAccountList(val string) error {
	v := strings.TrimSpace(val)
	if v == "" {
		return nil
	}
	for raw := range strings.SplitSeq(v, ",") {
		acct := strings.TrimSpace(raw)
		if len(acct) != 12 {
			return fmt.Errorf("account %q: expected 12 digits", acct)
		}
		for _, c := range acct {
			if c < '0' || c > '9' {
				return fmt.Errorf("account %q: must be digits only", acct)
			}
		}
	}
	return nil
}

// parseAccountList splits a comma-separated input into a trimmed slice.
// Empty input returns nil so the caller can omit the YAML key.
func parseAccountList(val string) []string {
	v := strings.TrimSpace(val)
	if v == "" {
		return nil
	}
	var out []string
	for raw := range strings.SplitSeq(v, ",") {
		if s := strings.TrimSpace(raw); s != "" {
			out = append(out, s)
		}
	}
	return out
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
