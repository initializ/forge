package steps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/initializ/forge/forge-cli/internal/tui"
	"github.com/initializ/forge/forge-cli/internal/wapair"
)

// WhatsappSessionTokenKey is the synthetic key the channel step uses to hand
// the paired session's temp path to scaffold, which moves the file into the
// project and strips the key before .env is written. Mirrors the existing
// __egress_domains / __custom_shape convention in cmd/init.go.
const WhatsappSessionTokenKey = "__whatsapp_session"

// qrMaxRows is the tallest QR we will draw inline. WhatsApp pairing payloads
// render to roughly 29 rows at QuietZone 1; anything beyond this plus the
// wizard's own chrome will not fit a normal terminal, and a wrapped QR is
// unscannable noise rather than a degraded picture.
const qrMaxRows = 34

// updateWhatsappPairPhase is the state machine for inline QR pairing. It
// handles the two async events (session started, pairing event) plus the
// retry/skip keys available in the error state.
func (s *ChannelStep) updateWhatsappPairPhase(msg tea.Msg) (tui.Step, tea.Cmd) {
	switch m := msg.(type) {
	case whatsappSessionStartedMsg:
		if m.err != nil {
			s.failWhatsappPair(m.err.Error())
			return s, nil
		}
		s.pairSession = m.session
		return s, s.nextWhatsappEventCmd()

	case whatsappPairEventMsg:
		if !m.ok {
			// Stream closed without a terminal event.
			s.failWhatsappPair("pairing ended unexpectedly")
			return s, nil
		}
		switch m.event.Kind {
		case wapair.EventScanned:
			// Stop showing a code the phone has already taken, and stop
			// offering skip: tearing the socket down now would abort the
			// handshake and leave a half-registered device.
			s.pairStatus = whatsappPairFinalizing
			s.pairQR = ""
			return s, s.nextWhatsappEventCmd()

		case wapair.EventQR:
			s.pairStatus = whatsappPairScanning
			s.pairQR = m.event.Code
			return s, s.nextWhatsappEventCmd()

		case wapair.EventPaired:
			s.pairJID = m.event.JID
			// Hand the temp session path to scaffold, which relocates it into
			// the project once the directory exists. Read from the step rather
			// than the session so the transition stays independent of whether
			// the socket is still open.
			s.tokens[WhatsappSessionTokenKey] = s.pairSessionPath
			s.closeWhatsappSession()
			s.complete = true
			return s, func() tea.Msg { return tui.StepCompleteMsg{} }

		case wapair.EventError:
			s.failWhatsappPair(m.event.Err.Error())
			return s, nil
		}
		return s, nil

	case tea.KeyMsg:
		switch m.String() {
		case "s", "S":
			// Skip — available while scanning as well as after a failure, since
			// a QR too tall for the terminal is only escapable this way.
			// Finishing unpaired is fine: the agent scaffolds, and the operator
			// pairs later with `forge channel whatsapp-login`.
			// Ignore while connecting (nothing to tear down yet) and while
			// finalizing (aborting mid-handshake is what produces a
			// half-registered device the server later rejects with 401).
			if s.pairStatus == whatsappPairConnecting || s.pairStatus == whatsappPairFinalizing {
				return s, nil
			}
			s.cleanupWhatsappTemp()
			s.complete = true
			return s, func() tea.Msg { return tui.StepCompleteMsg{} }
		case "r", "R":
			if s.pairStatus != whatsappPairErr {
				return s, nil
			}
			s.pairStatus = whatsappPairConnecting
			s.pairErr = ""
			s.pairQR = ""
			return s, s.startWhatsappPairCmd()
		}
	}

	return s, nil
}

// failWhatsappPair moves into the error state, releasing the socket first so a
// retry doesn't stack a second live connection on the same store.
func (s *ChannelStep) failWhatsappPair(msg string) {
	s.closeWhatsappSession()
	s.pairStatus = whatsappPairErr
	s.pairErr = msg
	s.pairQR = ""
}

// closeWhatsappSession releases the pairing socket and SQLite handle. The temp
// directory is deliberately left in place: on success scaffold still needs to
// read the session file out of it.
func (s *ChannelStep) closeWhatsappSession() {
	if s.pairSession != nil {
		_ = s.pairSession.Close()
		s.pairSession = nil
	}
}

// cleanupWhatsappTemp removes the temp store. Only safe on the abandon paths —
// never after a successful pairing, whose file scaffold has yet to copy.
func (s *ChannelStep) cleanupWhatsappTemp() {
	s.closeWhatsappSession()
	if s.pairTempDir != "" {
		_ = os.RemoveAll(s.pairTempDir)
		s.pairTempDir = ""
	}
	delete(s.tokens, WhatsappSessionTokenKey)
}

// startWhatsappPairCmd opens a pairing session against a temp store.
//
// The project directory does not exist yet — the wizard runs before scaffold —
// so the pairing cannot be written to its final location. It lands in a temp
// dir and scaffold relocates it.
func (s *ChannelStep) startWhatsappPairCmd() tea.Cmd {
	// Reuse the temp dir across retries so a failed attempt doesn't leak one.
	if s.pairTempDir == "" {
		dir, err := os.MkdirTemp("", "forge-whatsapp-pair-")
		if err != nil {
			return func() tea.Msg {
				return whatsappSessionStartedMsg{err: fmt.Errorf("creating temp session dir: %w", err)}
			}
		}
		s.pairTempDir = dir
	}
	// A retry must start from a clean store: whatsmeow will not re-issue QR
	// codes for a device row that is half-registered from a failed attempt.
	sessionPath := filepath.Join(s.pairTempDir, "whatsapp-session.db")
	_ = os.Remove(sessionPath)
	s.pairSessionPath = sessionPath

	return func() tea.Msg {
		sess, err := wapair.Start(context.Background(), sessionPath)
		return whatsappSessionStartedMsg{session: sess, err: err}
	}
}

// nextWhatsappEventCmd waits for one pairing event. Each event re-issues this
// command, so the QR refreshes as WhatsApp rotates codes.
func (s *ChannelStep) nextWhatsappEventCmd() tea.Cmd {
	sess := s.pairSession
	if sess == nil {
		return nil
	}
	return func() tea.Msg {
		evt, ok := <-sess.Events()
		return whatsappPairEventMsg{event: evt, ok: ok}
	}
}

func (s *ChannelStep) viewWhatsappPair() string {
	header := s.styles.SecondaryTxt.Render("WhatsApp Setup — link a device:")

	switch s.pairStatus {
	case whatsappPairConnecting:
		return fmt.Sprintf("  %s\n  %s\n\n",
			header,
			s.styles.AccentTxt.Render("⣾ Connecting to WhatsApp..."),
		)

	case whatsappPairScanning:
		var b strings.Builder
		fmt.Fprintf(&b, "  %s\n\n", header)
		fmt.Fprintf(&b, "  %s\n", s.styles.DimTxt.Render("On your phone: WhatsApp → Settings → Linked Devices →"))
		fmt.Fprintf(&b, "  %s\n\n", s.styles.DimTxt.Render("Link a Device, then scan this code:"))

		rows, cols := wapair.QRDimensions(s.pairQR)
		if rows > qrMaxRows {
			// Drawing it anyway would wrap into unscannable noise and push the
			// rest of the wizard off-screen. Point at the standalone command,
			// which owns the full terminal.
			fmt.Fprintf(&b, "  %s\n", s.styles.ErrorTxt.Render(
				fmt.Sprintf("The QR code needs %d×%d and does not fit here.", rows, cols)))
			fmt.Fprintf(&b, "  %s\n\n", s.styles.DimTxt.Render(
				"Press S to skip, then run `forge channel whatsapp-login`."))
		} else {
			var qr strings.Builder
			wapair.RenderQR(s.pairQR, &qr)
			for _, line := range strings.Split(strings.TrimRight(qr.String(), "\n"), "\n") {
				b.WriteString("  " + line + "\n")
			}
			b.WriteString("\n")
		}

		fmt.Fprintf(&b, "  %s\n", s.styles.ErrorTxt.Render("Use a DEDICATED number, not your personal one —"))
		fmt.Fprintf(&b, "  %s\n\n", s.styles.ErrorTxt.Render("WhatsApp may ban numbers used for automation."))
		fmt.Fprintf(&b, "  %s\n", s.styles.DimTxt.Render("⣾ Waiting for the scan. The code refreshes automatically."))
		fmt.Fprintf(&b, "  %s\n", s.styles.DimTxt.Render("(Press S to skip and pair later.)"))
		return b.String()

	case whatsappPairFinalizing:
		return fmt.Sprintf("  %s\n\n  %s\n  %s\n\n",
			header,
			s.styles.AccentTxt.Render("⣾ Scanned — finalizing the link with WhatsApp..."),
			s.styles.DimTxt.Render("Keep this running; interrupting now would leave the pairing incomplete."),
		)

	case whatsappPairErr:
		return fmt.Sprintf("  %s\n\n  %s\n  %s\n\n  %s\n  %s\n",
			header,
			s.styles.ErrorTxt.Render("✗ Pairing failed:"),
			s.styles.DimTxt.Render("    "+s.pairErr),
			s.styles.DimTxt.Render("Press R to retry, or S to skip and pair later with"),
			s.styles.DimTxt.Render("`forge channel whatsapp-login`."),
		)
	}
	return ""
}
