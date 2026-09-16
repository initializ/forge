package steps

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/initializ/forge/forge-cli/internal/tui"
	"github.com/initializ/forge/forge-cli/internal/wapair"
)

// newPairStep builds a step already sitting in the pairing phase, with the
// temp-store bookkeeping the network path would normally have set up.
func newPairStep(t *testing.T) *ChannelStep {
	t.Helper()
	s := NewChannelStep(tui.NewStyleSet(tui.DarkTheme), nil)
	s.channel = "whatsapp"
	s.phase = channelWhatsappPairPhase
	s.pairStatus = whatsappPairConnecting
	s.pairTempDir = t.TempDir()
	s.pairSessionPath = filepath.Join(s.pairTempDir, "whatsapp-session.db")
	return s
}

func TestChannelStep_SelectingWhatsappEntersPairPhase(t *testing.T) {
	s := NewChannelStep(tui.NewStyleSet(tui.DarkTheme), nil)
	s.channel = "whatsapp"
	s.phase = channelWhatsappPairPhase
	s.pairStatus = whatsappPairConnecting

	if got := s.View(80); !strings.Contains(got, "Connecting to WhatsApp") {
		t.Errorf("connecting view should say so, got %q", got)
	}
	if s.Complete() {
		t.Error("step must not be complete while connecting")
	}
}

func TestWhatsappPair_QREventRendersCode(t *testing.T) {
	s := newPairStep(t)

	_, _ = s.updateWhatsappPairPhase(whatsappPairEventMsg{
		ok:    true,
		event: wapair.Event{Kind: wapair.EventQR, Code: "2@abc123,def456,ghi789"},
	})

	if s.pairStatus != whatsappPairScanning {
		t.Fatalf("status = %v, want scanning", s.pairStatus)
	}
	view := s.View(80)
	if !strings.Contains(view, "Linked Devices") {
		t.Errorf("view should carry scan instructions, got %q", view)
	}
	if !strings.Contains(view, "█") && !strings.Contains(view, "▀") {
		t.Errorf("view should contain a rendered QR, got %q", view)
	}
}

// A refreshed code must replace the previous one, not stack.
func TestWhatsappPair_QRRefreshReplacesCode(t *testing.T) {
	s := newPairStep(t)
	for _, code := range []string{"2@first", "2@second"} {
		_, _ = s.updateWhatsappPairPhase(whatsappPairEventMsg{
			ok: true, event: wapair.Event{Kind: wapair.EventQR, Code: code},
		})
	}
	if s.pairQR != "2@second" {
		t.Errorf("pairQR = %q, want the latest code", s.pairQR)
	}
}

func TestWhatsappPair_PairedSetsSessionTokenAndCompletes(t *testing.T) {
	s := newPairStep(t)

	_, cmd := s.updateWhatsappPairPhase(whatsappPairEventMsg{
		ok:    true,
		event: wapair.Event{Kind: wapair.EventPaired, JID: "14155550100@s.whatsapp.net"},
	})

	if !s.Complete() {
		t.Error("step should be complete after pairing")
	}
	if got := s.tokens[WhatsappSessionTokenKey]; got != s.pairSessionPath {
		t.Errorf("session token = %q, want %q", got, s.pairSessionPath)
	}
	if cmd == nil {
		t.Fatal("expected a StepCompleteMsg command")
	}
	if _, ok := cmd().(tui.StepCompleteMsg); !ok {
		t.Error("expected StepCompleteMsg")
	}
}

// The temp store must survive a successful pairing — scaffold still has to
// copy the file out of it.
func TestWhatsappPair_PairedKeepsTempStore(t *testing.T) {
	s := newPairStep(t)
	tempDir := s.pairTempDir

	_, _ = s.updateWhatsappPairPhase(whatsappPairEventMsg{
		ok: true, event: wapair.Event{Kind: wapair.EventPaired},
	})

	if _, err := os.Stat(tempDir); err != nil {
		t.Errorf("temp store removed after pairing, scaffold cannot read it: %v", err)
	}
}

func TestWhatsappPair_ErrorEntersErrorState(t *testing.T) {
	s := newPairStep(t)

	_, _ = s.updateWhatsappPairPhase(whatsappPairEventMsg{
		ok:    true,
		event: wapair.Event{Kind: wapair.EventError, Err: errors.New("boom")},
	})

	if s.pairStatus != whatsappPairErr {
		t.Fatalf("status = %v, want error", s.pairStatus)
	}
	if s.Complete() {
		t.Error("an error must not complete the step")
	}
	view := s.View(80)
	for _, want := range []string{"Pairing failed", "boom", "whatsapp-login"} {
		if !strings.Contains(view, want) {
			t.Errorf("error view missing %q, got %q", want, view)
		}
	}
}

func TestWhatsappPair_SessionStartFailureEntersErrorState(t *testing.T) {
	s := newPairStep(t)
	_, _ = s.updateWhatsappPairPhase(whatsappSessionStartedMsg{err: errors.New("no socket")})

	if s.pairStatus != whatsappPairErr {
		t.Fatalf("status = %v, want error", s.pairStatus)
	}
	if !strings.Contains(s.pairErr, "no socket") {
		t.Errorf("pairErr = %q, want the underlying cause", s.pairErr)
	}
}

// A closed event stream with no terminal event must not hang the wizard.
func TestWhatsappPair_ClosedStreamFailsRatherThanHangs(t *testing.T) {
	s := newPairStep(t)
	_, _ = s.updateWhatsappPairPhase(whatsappPairEventMsg{ok: false})

	if s.pairStatus != whatsappPairErr {
		t.Fatalf("status = %v, want error", s.pairStatus)
	}
}

func TestWhatsappPair_SkipCompletesWithoutToken(t *testing.T) {
	s := newPairStep(t)
	s.pairStatus = whatsappPairScanning
	tempDir := s.pairTempDir

	_, _ = s.updateWhatsappPairPhase(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})

	if !s.Complete() {
		t.Error("skip should complete the step")
	}
	if _, ok := s.tokens[WhatsappSessionTokenKey]; ok {
		t.Error("skip must not leave a session token behind")
	}
	if _, err := os.Stat(tempDir); !os.IsNotExist(err) {
		t.Errorf("skip should remove the temp store, stat err = %v", err)
	}
}

// Skip is offered in the scanning view, so it has to work there — not only
// after a failure. A QR too tall for the terminal is escapable no other way.
func TestWhatsappPair_SkipWorksWhileScanning(t *testing.T) {
	s := newPairStep(t)
	s.pairStatus = whatsappPairScanning
	_, _ = s.updateWhatsappPairPhase(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'S'}})
	if !s.Complete() {
		t.Error("skip should work while scanning")
	}
}

// While connecting there is no session to tear down yet; a keypress then must
// be ignored rather than completing the step in a half-built state.
func TestWhatsappPair_SkipIgnoredWhileConnecting(t *testing.T) {
	s := newPairStep(t)
	s.pairStatus = whatsappPairConnecting
	_, _ = s.updateWhatsappPairPhase(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if s.Complete() {
		t.Error("skip must be ignored while still connecting")
	}
}

func TestWhatsappPair_RetryOnlyFromErrorState(t *testing.T) {
	s := newPairStep(t)

	s.pairStatus = whatsappPairScanning
	if _, cmd := s.updateWhatsappPairPhase(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}}); cmd != nil {
		t.Error("retry must be ignored while scanning")
	}

	s.pairStatus = whatsappPairErr
	s.pairErr = "boom"
	_, cmd := s.updateWhatsappPairPhase(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd == nil {
		t.Fatal("retry from the error state should start a new attempt")
	}
	if s.pairStatus != whatsappPairConnecting {
		t.Errorf("status = %v, want connecting after retry", s.pairStatus)
	}
	if s.pairErr != "" {
		t.Errorf("retry should clear the previous error, got %q", s.pairErr)
	}
}

// Retries must reuse one temp dir rather than leaking a new one each time.
func TestWhatsappPair_RetryReusesTempDir(t *testing.T) {
	s := newPairStep(t)
	first := s.pairTempDir

	s.pairStatus = whatsappPairErr
	_, _ = s.updateWhatsappPairPhase(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})

	if s.pairTempDir != first {
		t.Errorf("temp dir changed on retry: %q -> %q", first, s.pairTempDir)
	}
}

func TestChannelStep_WhatsappSummary(t *testing.T) {
	s := NewChannelStep(tui.NewStyleSet(tui.DarkTheme), nil)
	s.channel = "whatsapp"
	if got := s.Summary(); got != "WhatsApp" {
		t.Errorf("Summary() = %q, want %q", got, "WhatsApp")
	}
}

// The wizard has limited vertical room. A realistic WhatsApp pairing payload
// must render small enough to draw inline, or the QR guard silently degrades
// every pairing to the standalone command.
func TestWhatsappPair_RealisticQRFitsInline(t *testing.T) {
	// Shape and length match a real pairing code: four comma-separated
	// base64 segments, ~200 chars total.
	code := "2@" + strings.Repeat("A1b2C3d4", 8) + "," + strings.Repeat("E5f6G7h8", 6) +
		"," + strings.Repeat("I9j0K1l2", 5) + "," + strings.Repeat("M3n4O5p6", 2)

	rows, cols := wapair.QRDimensions(code)
	if rows > qrMaxRows {
		t.Errorf("QR renders %d rows, over the %d inline budget — pairing would always fall back", rows, qrMaxRows)
	}
	if cols > 80 {
		t.Errorf("QR renders %d cols, wider than an 80-col terminal", cols)
	}
	t.Logf("realistic pairing QR: %d rows x %d cols (payload %d chars)", rows, cols, len(code))
}

// After the phone accepts the code the QR must disappear — leaving it on
// screen invites a second scan of a dead code.
func TestWhatsappPair_ScannedEntersFinalizing(t *testing.T) {
	s := newPairStep(t)
	s.pairStatus = whatsappPairScanning
	s.pairQR = "2@stale"

	_, _ = s.updateWhatsappPairPhase(whatsappPairEventMsg{
		ok: true, event: wapair.Event{Kind: wapair.EventScanned},
	})

	if s.pairStatus != whatsappPairFinalizing {
		t.Fatalf("status = %v, want finalizing", s.pairStatus)
	}
	if s.pairQR != "" {
		t.Error("the scanned QR must be cleared")
	}
	view := s.View(80)
	if !strings.Contains(view, "finalizing") {
		t.Errorf("view should say it is finalizing, got %q", view)
	}
	if strings.Contains(view, "█") {
		t.Error("view must not still draw the scanned QR")
	}
}

// Skipping mid-handshake closes the socket before the post-pair login lands,
// which is exactly what produces a device the server rejects with 401. The
// key must be ignored in this window.
func TestWhatsappPair_SkipIgnoredWhileFinalizing(t *testing.T) {
	s := newPairStep(t)
	s.pairStatus = whatsappPairFinalizing

	_, _ = s.updateWhatsappPairPhase(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})

	if s.Complete() {
		t.Error("skip must be ignored while the pairing handshake is running")
	}
	if _, ok := s.tokens[WhatsappSessionTokenKey]; ok {
		t.Error("no session token should be set mid-handshake")
	}
}

// The finalizing view must not promise a skip key that is deliberately inert.
func TestWhatsappPair_FinalizingViewDoesNotOfferSkip(t *testing.T) {
	s := newPairStep(t)
	s.pairStatus = whatsappPairFinalizing
	if view := s.View(80); strings.Contains(view, "Press S") {
		t.Errorf("finalizing view must not offer skip, got %q", view)
	}
}
