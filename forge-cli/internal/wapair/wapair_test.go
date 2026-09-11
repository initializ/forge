package wapair

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	testSettle  = 20 * time.Millisecond
	testTimeout = 500 * time.Millisecond
)

func loggedIn() bool  { return true }
func loggedOut() bool { return false }
func testJID() string { return "14155550100@s.whatsapp.net" }

// The regression this guards: PairSuccess is not the end of pairing. Reporting
// success before the post-pair reconnect leaves the device half-registered,
// and the next connection is dropped with 401 "logged out from another
// device". Completion must wait for the reconnect.
func TestAwaitCompletion_WaitsForPostPairConnect(t *testing.T) {
	connected := make(chan struct{})
	fatal := make(chan error, 1)

	var got Event
	done := make(chan struct{})
	go func() {
		got = awaitCompletion(context.Background(), connected, fatal, loggedIn, testJID, testSettle, testTimeout)
		close(done)
	}()

	// Nothing may be reported while the reconnect is still outstanding.
	select {
	case <-done:
		t.Fatal("reported completion before the post-pair reconnect")
	case <-time.After(50 * time.Millisecond):
	}

	close(connected)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("did not complete after the reconnect")
	}

	if got.Kind != EventPaired {
		t.Fatalf("kind = %v, want EventPaired (err=%v)", got.Kind, got.Err)
	}
	if got.JID != testJID() {
		t.Errorf("JID = %q, want %q", got.JID, testJID())
	}
}

// The settle window exists so the asynchronous prekey upload can finish;
// completion must not be reported before it elapses.
func TestAwaitCompletion_HoldsSocketForSettleWindow(t *testing.T) {
	connected := make(chan struct{})
	close(connected)

	start := time.Now()
	got := awaitCompletion(context.Background(), connected, make(chan error, 1),
		loggedIn, testJID, 150*time.Millisecond, testTimeout)

	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Errorf("returned after %v, expected to hold for the settle window", elapsed)
	}
	if got.Kind != EventPaired {
		t.Errorf("kind = %v, want EventPaired", got.Kind)
	}
}

// A logout arriving during the wait is the server rejecting the pairing.
func TestAwaitCompletion_LogoutBeforeConnectIsFatal(t *testing.T) {
	fatal := make(chan error, 1)
	fatal <- errors.New("server rejected the new pairing (401)")

	got := awaitCompletion(context.Background(), make(chan struct{}), fatal,
		loggedIn, testJID, testSettle, testTimeout)

	if got.Kind != EventError {
		t.Fatalf("kind = %v, want EventError", got.Kind)
	}
	if !strings.Contains(got.Err.Error(), "rejected") {
		t.Errorf("err = %v, want the rejection reason", got.Err)
	}
}

// A logout can also land inside the settle window, after a good connect.
func TestAwaitCompletion_LogoutDuringSettleIsFatal(t *testing.T) {
	connected := make(chan struct{})
	close(connected)
	fatal := make(chan error, 1)

	go func() {
		time.Sleep(20 * time.Millisecond)
		fatal <- errors.New("logged out from another device")
	}()

	got := awaitCompletion(context.Background(), connected, fatal,
		loggedIn, testJID, 2*time.Second, testTimeout)

	if got.Kind != EventError {
		t.Fatalf("kind = %v, want EventError", got.Kind)
	}
	if !strings.Contains(got.Err.Error(), "logged out") {
		t.Errorf("err = %v, want the logout reason", got.Err)
	}
}

// Connected then not-logged-in means the handshake did not actually land.
// Reporting success there would hand back an unusable session.
func TestAwaitCompletion_NotLoggedInAfterSettleFails(t *testing.T) {
	connected := make(chan struct{})
	close(connected)

	got := awaitCompletion(context.Background(), connected, make(chan error, 1),
		loggedOut, testJID, testSettle, testTimeout)

	if got.Kind != EventError {
		t.Fatalf("kind = %v, want EventError", got.Kind)
	}
	if !strings.Contains(got.Err.Error(), "did not complete") {
		t.Errorf("err = %v, want a completion failure", got.Err)
	}
}

func TestAwaitCompletion_TimesOutWaitingForConnect(t *testing.T) {
	got := awaitCompletion(context.Background(), make(chan struct{}), make(chan error, 1),
		loggedIn, testJID, testSettle, 60*time.Millisecond)

	if got.Kind != EventError {
		t.Fatalf("kind = %v, want EventError", got.Kind)
	}
	if !strings.Contains(got.Err.Error(), "timed out") {
		t.Errorf("err = %v, want a timeout", got.Err)
	}
}

func TestAwaitCompletion_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got := awaitCompletion(ctx, make(chan struct{}), make(chan error, 1),
		loggedIn, testJID, testSettle, testTimeout)

	if got.Kind != EventError {
		t.Fatalf("kind = %v, want EventError", got.Kind)
	}
	if !errors.Is(got.Err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", got.Err)
	}
}

// The real settle window has to be long enough to be meaningful and short
// enough not to look like a hang.
func TestPairTimings_AreSane(t *testing.T) {
	if pairSettleDelay < time.Second {
		t.Errorf("pairSettleDelay = %v, too short for the prekey upload to land", pairSettleDelay)
	}
	if pairSettleDelay > 15*time.Second {
		t.Errorf("pairSettleDelay = %v, long enough to read as a hang", pairSettleDelay)
	}
	if pairCompletionTimeout <= pairSettleDelay {
		t.Errorf("pairCompletionTimeout (%v) must exceed pairSettleDelay (%v)", pairCompletionTimeout, pairSettleDelay)
	}
}

func TestRenderQR_ProducesBlockGlyphs(t *testing.T) {
	var b strings.Builder
	RenderQR("2@test,payload,here", &b)
	out := b.String()
	if !strings.Contains(out, "█") && !strings.Contains(out, "▀") {
		t.Errorf("expected block glyphs, got %q", out[:min(80, len(out))])
	}
}

func TestQRDimensions_MatchesRenderedOutput(t *testing.T) {
	const code = "2@abcdefgh,ijklmnop,qrstuvwx"

	rows, cols := QRDimensions(code)

	var b strings.Builder
	RenderQR(code, &b)
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")

	if rows != len(lines) {
		t.Errorf("QRDimensions rows = %d, rendered %d lines", rows, len(lines))
	}
	widest := 0
	for _, l := range lines {
		if n := len([]rune(l)); n > widest {
			widest = n
		}
	}
	if cols != widest {
		t.Errorf("QRDimensions cols = %d, widest rendered line = %d", cols, widest)
	}
}

// A tighter quiet zone is what makes the QR fit inside the init wizard.
func TestRenderQR_UsesTightQuietZone(t *testing.T) {
	code := "2@" + strings.Repeat("A1b2C3d4", 20)
	rows, cols := QRDimensions(code)
	if rows > 34 || cols > 80 {
		t.Errorf("QR is %dx%d — too large to draw inline in the wizard", rows, cols)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
