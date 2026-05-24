package steps

import (
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/initializ/forge/forge-cli/internal/tui"
)

// newTestAuthStep wires up an AuthStep with a default StyleSet so we can
// drive it without spinning up a real terminal.
func newTestAuthStep(t *testing.T) *AuthStep {
	t.Helper()
	styles := tui.NewStyleSet(tui.DarkTheme)
	return NewAuthStep(styles)
}

// press injects a key event into the step and returns the updated step.
func press(t *testing.T, s *AuthStep, key string) *AuthStep {
	t.Helper()
	var msg tea.KeyMsg
	switch key {
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		msg = tea.KeyMsg{Type: tea.KeyUp}
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "backspace":
		msg = tea.KeyMsg{Type: tea.KeyBackspace}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	updated, _ := s.Update(msg)
	return updated.(*AuthStep)
}

// typeIn injects each rune of the string as a single KeyRunes message.
func typeIn(t *testing.T, s *AuthStep, text string) *AuthStep {
	t.Helper()
	for _, r := range text {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		updated, _ := s.Update(msg)
		s = updated.(*AuthStep)
	}
	return s
}

// --- Title / Icon ---

func TestAuthStep_TitleAndIcon(t *testing.T) {
	s := newTestAuthStep(t)
	if s.Title() == "" {
		t.Error("Title is empty")
	}
	if s.Icon() == "" {
		t.Error("Icon is empty")
	}
}

// --- "None" path completes immediately ---

func TestAuthStep_NoneIsDefault(t *testing.T) {
	s := newTestAuthStep(t)
	s = press(t, s, "enter") // Confirm "None" (first item)
	if !s.Complete() {
		t.Error("expected Complete after selecting None")
	}

	ctx := tui.NewWizardContext()
	s.Apply(ctx)
	if ctx.AuthMode != AuthModeNone {
		t.Errorf("AuthMode = %q, want %q", ctx.AuthMode, AuthModeNone)
	}
	if ctx.AuthSettings != nil {
		t.Errorf("AuthSettings = %v, want nil", ctx.AuthSettings)
	}
	if len(ctx.AuthEgressHosts) != 0 {
		t.Errorf("AuthEgressHosts = %v, want empty", ctx.AuthEgressHosts)
	}
}

// --- "Custom" path completes immediately with no settings ---

func TestAuthStep_Custom(t *testing.T) {
	s := newTestAuthStep(t)
	// Picker order: 0=None 1=OIDC 2=HTTPVerifier 3=AWS 4=GCP 5=AAD 6=Custom.
	// Six downs to reach Custom at index 6.
	for range 6 {
		s = press(t, s, "down")
	}
	s = press(t, s, "enter")
	if !s.Complete() {
		t.Fatal("expected Complete after selecting Custom")
	}

	ctx := tui.NewWizardContext()
	s.Apply(ctx)
	if ctx.AuthMode != AuthModeCustom {
		t.Errorf("AuthMode = %q, want %q", ctx.AuthMode, AuthModeCustom)
	}
	if ctx.AuthSettings != nil {
		t.Errorf("AuthSettings = %v, want nil for custom mode", ctx.AuthSettings)
	}
}

// --- OIDC full path ---

func TestAuthStep_OIDC_FullFlow(t *testing.T) {
	s := newTestAuthStep(t)

	// Pick OIDC (index 1).
	s = press(t, s, "down")
	s = press(t, s, "enter")
	if s.phase != authOIDCIssuerPhase {
		t.Fatalf("phase = %v, want issuer", s.phase)
	}

	// Type issuer.
	s = typeIn(t, s, "https://login.example.com/")
	s = press(t, s, "enter")
	if s.phase != authOIDCAudiencePhase {
		t.Fatalf("phase = %v, want audience", s.phase)
	}

	// Type audience.
	s = typeIn(t, s, "api://forge")
	s = press(t, s, "enter")
	if s.phase != authOIDCGroupsClaimPhase {
		t.Fatalf("phase = %v, want groups claim", s.phase)
	}

	// Accept default groups claim.
	s = press(t, s, "enter")
	if !s.Complete() {
		t.Fatal("expected Complete after groups claim")
	}

	ctx := tui.NewWizardContext()
	s.Apply(ctx)
	if ctx.AuthMode != AuthModeOIDC {
		t.Errorf("AuthMode = %q, want oidc", ctx.AuthMode)
	}
	if ctx.AuthSettings["issuer"] != "https://login.example.com" {
		t.Errorf("issuer = %v, want trimmed trailing slash", ctx.AuthSettings["issuer"])
	}
	if ctx.AuthSettings["audience"] != "api://forge" {
		t.Errorf("audience = %v", ctx.AuthSettings["audience"])
	}
	if _, ok := ctx.AuthSettings["claim_map"]; ok {
		t.Errorf("claim_map should NOT be emitted when groups claim is default")
	}
	if !reflect.DeepEqual(ctx.AuthEgressHosts, []string{"login.example.com"}) {
		t.Errorf("AuthEgressHosts = %v, want [login.example.com]", ctx.AuthEgressHosts)
	}
}

func TestAuthStep_OIDC_CustomGroupsClaim(t *testing.T) {
	s := newTestAuthStep(t)
	s = press(t, s, "down")
	s = press(t, s, "enter") // OIDC
	s = typeIn(t, s, "https://login.example.com")
	s = press(t, s, "enter")
	s = typeIn(t, s, "api://forge")
	s = press(t, s, "enter")
	s = typeIn(t, s, "roles") // non-default groups claim
	s = press(t, s, "enter")

	ctx := tui.NewWizardContext()
	s.Apply(ctx)
	cm, ok := ctx.AuthSettings["claim_map"].(map[string]any)
	if !ok {
		t.Fatalf("claim_map not present or wrong type: %v", ctx.AuthSettings)
	}
	if cm["groups"] != "roles" {
		t.Errorf("claim_map.groups = %v, want roles", cm["groups"])
	}
}

// --- HTTP Verifier full path ---

func TestAuthStep_HTTPVerifier_FullFlow(t *testing.T) {
	s := newTestAuthStep(t)

	// Pick HTTP Verifier (index 2).
	s = press(t, s, "down")
	s = press(t, s, "down")
	s = press(t, s, "enter")
	if s.phase != authHTTPURLPhase {
		t.Fatalf("phase = %v, want url", s.phase)
	}

	s = typeIn(t, s, "https://verify.example.com/verify")
	s = press(t, s, "enter")
	if s.phase != authHTTPOrgPhase {
		t.Fatalf("phase = %v, want org", s.phase)
	}

	// Skip optional default org.
	s = press(t, s, "enter")
	if !s.Complete() {
		t.Fatal("expected Complete")
	}

	ctx := tui.NewWizardContext()
	s.Apply(ctx)
	if ctx.AuthMode != AuthModeHTTPVerifier {
		t.Errorf("AuthMode = %q, want http_verifier", ctx.AuthMode)
	}
	if ctx.AuthSettings["url"] != "https://verify.example.com/verify" {
		t.Errorf("url = %v", ctx.AuthSettings["url"])
	}
	if _, ok := ctx.AuthSettings["default_org"]; ok {
		t.Errorf("default_org should not be emitted when blank")
	}
}

func TestAuthStep_HTTPVerifier_WithDefaultOrg(t *testing.T) {
	s := newTestAuthStep(t)
	s = press(t, s, "down")
	s = press(t, s, "down")
	s = press(t, s, "enter") // HTTP Verifier
	s = typeIn(t, s, "https://verify.example.com/verify")
	s = press(t, s, "enter")
	s = typeIn(t, s, "acme")
	s = press(t, s, "enter")

	ctx := tui.NewWizardContext()
	s.Apply(ctx)
	if ctx.AuthSettings["default_org"] != "acme" {
		t.Errorf("default_org = %v, want acme", ctx.AuthSettings["default_org"])
	}
}

// --- Summary text ---

func TestAuthStep_Summary(t *testing.T) {
	tests := []struct {
		mode string
		want string
	}{
		{"", "Anonymous (no auth)"},
		{AuthModeNone, "Anonymous (no auth)"},
		{AuthModeOIDC, "OIDC"},
		{AuthModeCustom, "Custom (edit forge.yaml)"},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			s := newTestAuthStep(t)
			s.mode = tt.mode
			if got := s.Summary(); got != tt.want {
				t.Errorf("Summary = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- Helpers ---

func TestValidateHTTPSURL(t *testing.T) {
	tests := []struct {
		val string
		ok  bool
	}{
		{"", false},
		{"https://example.com", true},
		{"http://localhost:8080", true},
		{"login.example.com", false}, // no scheme
		{"ftp://example.com", false}, // wrong scheme
		{"https://", false},          // no host
	}
	for _, tt := range tests {
		t.Run(tt.val, func(t *testing.T) {
			err := validateHTTPSURL(tt.val)
			if tt.ok && err != nil {
				t.Errorf("expected ok, got %v", err)
			}
			if !tt.ok && err == nil {
				t.Errorf("expected error, got nil")
			}
		})
	}
}

func TestHostnameOrEmpty(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"https://login.example.com", "login.example.com"},
		{"http://localhost:8080/realms/dev", "localhost"},
		{"not a url", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := hostnameOrEmpty(tt.in); got != tt.want {
				t.Errorf("hostnameOrEmpty(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// --- Wizard interaction tests (review #12.6, #12.7) ---

func TestAuthStep_BackspaceOnEmptyInputReturnsToPicker(t *testing.T) {
	// Review #12.6: backspace-on-empty is the documented escape hatch
	// from a sub-step input back to the provider picker, but until
	// review #11e there was no visible hint AND no test. The hint was
	// added; this test pins the behavior.
	s := newTestAuthStep(t)

	// Walk into OIDC issuer phase.
	s = press(t, s, "down") // OIDC
	s = press(t, s, "enter")
	if s.phase != authOIDCIssuerPhase {
		t.Fatalf("setup: phase = %v, want issuer", s.phase)
	}

	// Backspace on EMPTY input → returns to picker.
	s = press(t, s, "backspace")
	if s.phase != authSelectPhase {
		t.Errorf("backspace-on-empty: phase = %v, want authSelectPhase", s.phase)
	}
	if s.mode != "" {
		t.Errorf("backspace-on-empty: mode = %q, want \"\" (reset)", s.mode)
	}
}

func TestAuthStep_BackspaceWithContentDoesNotReturnToPicker(t *testing.T) {
	// Counterpart: backspace with TEXT in the input only deletes the
	// last char — it must not escape out of the sub-step.
	s := newTestAuthStep(t)
	s = press(t, s, "down") // OIDC
	s = press(t, s, "enter")
	s = typeIn(t, s, "https://example.com")
	if s.phase != authOIDCIssuerPhase {
		t.Fatalf("setup: phase = %v, want issuer", s.phase)
	}

	s = press(t, s, "backspace")
	if s.phase != authOIDCIssuerPhase {
		t.Errorf("backspace-with-content escaped to phase %v, expected to stay on issuer", s.phase)
	}
}

func TestAuthStep_InvalidIssuerURLBlocksAdvance(t *testing.T) {
	// Review #12.7: validateHTTPSURL is unit-tested standalone, but
	// the wizard's gating on bad input was never asserted as an
	// interaction. Drive the step with a bad URL and confirm Enter
	// doesn't advance to the audience sub-step.
	s := newTestAuthStep(t)
	s = press(t, s, "down") // OIDC
	s = press(t, s, "enter")

	// Type something that fails validateHTTPSURL (no scheme).
	s = typeIn(t, s, "login.example.com")
	s = press(t, s, "enter")

	// MUST still be on the issuer phase — the bad URL blocked advance.
	if s.phase != authOIDCIssuerPhase {
		t.Errorf("invalid URL advanced past issuer phase to %v", s.phase)
	}

	// And the step must not be complete — Review/scaffold MUST NOT run
	// for a half-configured OIDC entry.
	if s.Complete() {
		t.Errorf("step reported complete with invalid issuer URL")
	}

	// Now type a valid replacement and confirm the gate releases.
	// Clear the input first by sending backspaces until empty — easier
	// than reaching into TextInput state.
	for range len("login.example.com") {
		s = press(t, s, "backspace")
	}
	if s.phase != authOIDCIssuerPhase {
		// Bug guard: a long sequence of backspaces should NOT escape to
		// the picker mid-way (only an Enter-after-empty would).
		t.Fatalf("backspaces escaped to phase %v unexpectedly", s.phase)
	}
	s = typeIn(t, s, "https://login.example.com")
	s = press(t, s, "enter")
	if s.phase != authOIDCAudiencePhase {
		t.Errorf("valid URL did not advance: phase = %v, want audience", s.phase)
	}
}

func TestAppendUnique(t *testing.T) {
	out := appendUnique([]string{"a", "b"}, "a")
	if !reflect.DeepEqual(out, []string{"a", "b"}) {
		t.Errorf("appendUnique duplicate failed: %v", out)
	}
	out = appendUnique([]string{"a"}, "b")
	if !reflect.DeepEqual(out, []string{"a", "b"}) {
		t.Errorf("appendUnique new failed: %v", out)
	}
}

// --- Phase 2: aws_sigv4 / gcp_iap / azure_ad ---

func TestAuthStep_AWSSigv4_FullFlow_WithAccounts(t *testing.T) {
	s := newTestAuthStep(t)

	// Picker order: 0=None 1=OIDC 2=HTTPVerifier 3=AWS 4=GCP 5=AAD 6=Custom
	// Navigate to AWS (3 downs).
	s = press(t, s, "down")
	s = press(t, s, "down")
	s = press(t, s, "down")
	s = press(t, s, "enter")
	if s.phase != authAWSRegionPhase {
		t.Fatalf("phase = %v, want AWS region", s.phase)
	}

	// Region
	s = typeIn(t, s, "us-east-1")
	s = press(t, s, "enter")
	if s.phase != authAWSAudiencePhase {
		t.Fatalf("phase = %v, want AWS audience", s.phase)
	}

	// Audience (optional — press Enter to skip)
	s = typeIn(t, s, "api://forge")
	s = press(t, s, "enter")
	if s.phase != authAWSAccountsPhase {
		t.Fatalf("phase = %v, want AWS accounts", s.phase)
	}

	// Accounts (comma-separated)
	s = typeIn(t, s, "412664885516, 109887654321")
	s = press(t, s, "enter")
	if !s.Complete() {
		t.Fatal("expected Complete after accounts")
	}

	ctx := tui.NewWizardContext()
	s.Apply(ctx)
	if ctx.AuthMode != AuthModeAWSSigv4 {
		t.Errorf("AuthMode = %q, want aws_sigv4", ctx.AuthMode)
	}
	if ctx.AuthSettings["region"] != "us-east-1" {
		t.Errorf("region = %v", ctx.AuthSettings["region"])
	}
	if ctx.AuthSettings["audience"] != "api://forge" {
		t.Errorf("audience = %v", ctx.AuthSettings["audience"])
	}
	accts, _ := ctx.AuthSettings["allowed_accounts"].([]string)
	if !reflect.DeepEqual(accts, []string{"412664885516", "109887654321"}) {
		t.Errorf("allowed_accounts = %v", accts)
	}
	if !reflect.DeepEqual(ctx.AuthEgressHosts, []string{"sts.us-east-1.amazonaws.com"}) {
		t.Errorf("egress hosts = %v", ctx.AuthEgressHosts)
	}
}

func TestAuthStep_AWSSigv4_AllowAudienceSkip(t *testing.T) {
	// Skipping the audience field (pressing Enter on empty input) and
	// the accounts field should still complete cleanly.
	s := newTestAuthStep(t)
	s = press(t, s, "down")
	s = press(t, s, "down")
	s = press(t, s, "down")
	s = press(t, s, "enter") // pick AWS
	s = typeIn(t, s, "us-east-1")
	s = press(t, s, "enter") // region → audience
	s = press(t, s, "enter") // audience (empty) → accounts
	s = press(t, s, "enter") // accounts (empty) → done
	if !s.Complete() {
		t.Fatal("expected Complete after skipping optional fields")
	}
	ctx := tui.NewWizardContext()
	s.Apply(ctx)
	if _, ok := ctx.AuthSettings["audience"]; ok {
		t.Errorf("audience should be omitted when empty: %v", ctx.AuthSettings)
	}
	if _, ok := ctx.AuthSettings["allowed_accounts"]; ok {
		t.Errorf("allowed_accounts should be omitted when empty: %v", ctx.AuthSettings)
	}
}

func TestAuthStep_AWSSigv4_RejectsMalformedAccount(t *testing.T) {
	// validateAccountList enforces the 12-digit shape before letting
	// the user advance past the accounts field.
	if err := validateAccountList("notanaccount"); err == nil {
		t.Error("expected error on malformed account ID")
	}
	if err := validateAccountList("412664885516,bad,109887654321"); err == nil {
		t.Error("expected error when any account in the list is bad")
	}
	if err := validateAccountList(""); err != nil {
		t.Errorf("empty input should be allowed (optional field), got %v", err)
	}
	if err := validateAccountList("412664885516 ,  109887654321 "); err != nil {
		t.Errorf("trimmed whitespace should be tolerated, got %v", err)
	}
}

func TestAuthStep_GCPIAP_FullFlow(t *testing.T) {
	s := newTestAuthStep(t)
	// 4 downs to reach GCP IAP.
	s = press(t, s, "down")
	s = press(t, s, "down")
	s = press(t, s, "down")
	s = press(t, s, "down")
	s = press(t, s, "enter")
	if s.phase != authGCPIAPAudiencePhase {
		t.Fatalf("phase = %v, want GCP audience", s.phase)
	}

	s = typeIn(t, s, "/projects/12345/global/backendServices/67890")
	s = press(t, s, "enter")
	if !s.Complete() {
		t.Fatal("expected Complete after audience")
	}

	ctx := tui.NewWizardContext()
	s.Apply(ctx)
	if ctx.AuthMode != AuthModeGCPIAP {
		t.Errorf("AuthMode = %q, want gcp_iap", ctx.AuthMode)
	}
	if ctx.AuthSettings["audience"] != "/projects/12345/global/backendServices/67890" {
		t.Errorf("audience = %v", ctx.AuthSettings["audience"])
	}
	if !reflect.DeepEqual(ctx.AuthEgressHosts, []string{"www.gstatic.com"}) {
		t.Errorf("egress hosts = %v, want [www.gstatic.com]", ctx.AuthEgressHosts)
	}
}

func TestAuthStep_AzureAD_FullFlow(t *testing.T) {
	s := newTestAuthStep(t)
	// 5 downs to reach Azure AD.
	for range 5 {
		s = press(t, s, "down")
	}
	s = press(t, s, "enter")
	if s.phase != authAADTenantPhase {
		t.Fatalf("phase = %v, want AAD tenant", s.phase)
	}

	s = typeIn(t, s, "00000000-1111-2222-3333-444444444444")
	s = press(t, s, "enter")
	if s.phase != authAADAudiencePhase {
		t.Fatalf("phase = %v, want AAD audience", s.phase)
	}

	s = typeIn(t, s, "api://forge")
	s = press(t, s, "enter")
	if !s.Complete() {
		t.Fatal("expected Complete after audience")
	}

	ctx := tui.NewWizardContext()
	s.Apply(ctx)
	if ctx.AuthMode != AuthModeAzureAD {
		t.Errorf("AuthMode = %q", ctx.AuthMode)
	}
	if ctx.AuthSettings["tenant_id"] != "00000000-1111-2222-3333-444444444444" {
		t.Errorf("tenant_id = %v", ctx.AuthSettings["tenant_id"])
	}
	if ctx.AuthSettings["audience"] != "api://forge" {
		t.Errorf("audience = %v", ctx.AuthSettings["audience"])
	}
	if _, ok := ctx.AuthSettings["allow_multi_tenant"]; ok {
		t.Errorf("TUI must NOT set allow_multi_tenant — multi-tenant requires YAML edit (security)")
	}
	if !reflect.DeepEqual(ctx.AuthEgressHosts, []string{"login.microsoftonline.com"}) {
		t.Errorf("egress hosts = %v", ctx.AuthEgressHosts)
	}
}

func TestAuthStep_Summary_Phase2(t *testing.T) {
	cases := []struct {
		setup func(*AuthStep)
		want  string
	}{
		{setup: func(s *AuthStep) { s.mode = AuthModeAWSSigv4; s.awsRegion = "us-east-1" }, want: "AWS Sigv4 · us-east-1"},
		{setup: func(s *AuthStep) {
			s.mode = AuthModeAWSSigv4
			s.awsRegion = "us-east-1"
			s.awsAccounts = []string{"A", "B"}
		}, want: "AWS Sigv4 · us-east-1 · 2 account(s)"},
		{setup: func(s *AuthStep) { s.mode = AuthModeGCPIAP }, want: "GCP IAP"},
		{setup: func(s *AuthStep) { s.mode = AuthModeAzureAD }, want: "Azure AD · single-tenant"},
	}
	for _, tc := range cases {
		s := newTestAuthStep(t)
		tc.setup(s)
		if got := s.Summary(); got != tc.want {
			t.Errorf("Summary = %q, want %q", got, tc.want)
		}
	}
}

func TestValidateAWSRegion(t *testing.T) {
	ok := []string{"us-east-1", "eu-west-2", "ap-southeast-1", "ca-central-1"}
	for _, s := range ok {
		if err := validateAWSRegion(s); err != nil {
			t.Errorf("validateAWSRegion(%q) = %v, want nil", s, err)
		}
	}
	bad := []string{"", "us", "us-east", "useast1"}
	for _, s := range bad {
		if err := validateAWSRegion(s); err == nil {
			t.Errorf("validateAWSRegion(%q) returned nil", s)
		}
	}
}

func TestParseAccountList(t *testing.T) {
	cases := map[string][]string{
		"":             nil,
		"   ":          nil,
		"412664885516": {"412664885516"},
		"a, b ,c":      {"a", "b", "c"},
		"a,,b":         {"a", "b"},
	}
	for in, want := range cases {
		got := parseAccountList(in)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("parseAccountList(%q) = %v, want %v", in, got, want)
		}
	}
}
