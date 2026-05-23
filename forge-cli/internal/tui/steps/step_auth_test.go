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
	// Move down 3 times to reach "Custom" (index 3).
	s = press(t, s, "down")
	s = press(t, s, "down")
	s = press(t, s, "down")
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
