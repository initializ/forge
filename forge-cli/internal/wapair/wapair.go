// Package wapair drives the WhatsApp Web QR pairing flow.
//
// It is the WhatsApp counterpart to internal/devicecode: a small, UI-agnostic
// driver that both `forge channel whatsapp-login` and the init wizard's
// channel step use, so the pairing state machine lives in exactly one place.
package wapair

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/initializ/forge/forge-plugins/channels/whatsapp"
)

// EventKind discriminates the events a pairing session emits.
type EventKind int

const (
	// EventQR carries a new code to display. WhatsApp rotates codes every
	// ~20s, so several of these arrive before the user finishes scanning.
	EventQR EventKind = iota
	// EventScanned means the phone accepted the code and the post-pair
	// handshake is running. NOT yet usable — EventPaired is the completion.
	EventScanned
	// EventPaired means the scan succeeded and the session is persisted.
	EventPaired
	// EventError is terminal: the session cannot complete.
	EventError
)

// Event is one step of the pairing flow.
type Event struct {
	Kind    EventKind
	Code    string        // EventQR: the raw payload to encode
	Timeout time.Duration // EventQR: how long until the next code
	JID     string        // EventPaired: the linked account
	Err     error         // EventError
}

// Session is an in-flight pairing attempt. Close it when done — it holds an
// open socket and a SQLite handle.
type Session struct {
	sessionPath string
	container   io.Closer
	client      *whatsmeow.Client
	events      chan Event
	cancel      context.CancelFunc
	closeOnce   sync.Once

	// Post-pair completion signals. PairSuccess is NOT the end of pairing:
	// the server drops the socket, the client reconnects and completes a
	// login handshake, and only then is the device registered server-side.
	connected chan struct{}
	connOnce  sync.Once
	fatal     chan error
}

// Start opens (creating if absent) the session store at sessionPath and begins
// a pairing attempt. Events are delivered on Events() until the channel closes.
//
// The caller owns the returned Session and must Close it.
func Start(ctx context.Context, sessionPath string) (*Session, error) {
	ctx, cancel := context.WithCancel(ctx)

	container, device, err := whatsapp.NewSessionDevice(ctx, sessionPath)
	if err != nil {
		cancel()
		return nil, err
	}

	client := whatsmeow.NewClient(device, nil)

	s := &Session{
		sessionPath: sessionPath,
		container:   container,
		client:      client,
		events:      make(chan Event, 4),
		cancel:      cancel,
		connected:   make(chan struct{}),
		fatal:       make(chan error, 1),
	}

	// Watch for the post-pair reconnect. The QR channel cannot report it: it
	// closes on PairSuccess and treats a later Connected as an unexpected
	// event, so completion has to be observed on the client itself.
	client.AddEventHandler(func(evt any) {
		switch e := evt.(type) {
		case *events.Connected:
			s.connOnce.Do(func() { close(s.connected) })
		case *events.LoggedOut:
			s.failFast(fmt.Errorf("server rejected the new pairing (%s)", e.Reason))
		case *events.ConnectFailure:
			s.failFast(fmt.Errorf("connection failed after pairing (%s)", e.Reason))
		}
	})

	// GetQRChannel must be called BEFORE Connect: pairing codes arrive on the
	// socket immediately after it opens, and a channel registered afterwards
	// misses them.
	qrChan, err := client.GetQRChannel(ctx)
	if err != nil {
		cancel()
		_ = container.Close()
		return nil, fmt.Errorf("whatsapp: opening QR channel: %w", err)
	}
	if err := client.Connect(); err != nil {
		cancel()
		_ = container.Close()
		return nil, fmt.Errorf("whatsapp: connecting: %w", err)
	}

	go s.pump(ctx, qrChan)
	return s, nil
}

// failFast records a terminal post-pair failure without blocking the emitter.
func (s *Session) failFast(err error) {
	select {
	case s.fatal <- err:
	default:
	}
}

// pump translates whatsmeow's QR items into Events and closes the channel when
// the flow reaches a terminal state.
func (s *Session) pump(ctx context.Context, qrChan <-chan whatsmeow.QRChannelItem) {
	defer close(s.events)

	for evt := range qrChan {
		switch evt.Event {
		case "code":
			s.emit(ctx, Event{Kind: EventQR, Code: evt.Code, Timeout: evt.Timeout})

		case "success":
			// The phone accepted the code, but pairing is NOT done — see
			// awaitPairCompletion. Tell the caller so it can stop showing a
			// QR that has already been scanned.
			s.emit(ctx, Event{Kind: EventScanned})
			s.emit(ctx, s.awaitPairCompletion(ctx))
			return

		case "timeout":
			s.emit(ctx, Event{Kind: EventError, Err: fmt.Errorf("QR code expired without being scanned")})
			return

		case "err-client-outdated":
			s.emit(ctx, Event{Kind: EventError, Err: fmt.Errorf("WhatsApp rejected this client as outdated — the pinned whatsmeow version needs updating")})
			return

		case "err-scanned-without-multidevice":
			s.emit(ctx, Event{Kind: EventError, Err: fmt.Errorf("the account scanned the code without multi-device enabled — enable it in WhatsApp → Settings → Linked Devices")})
			return

		case "error":
			s.emit(ctx, Event{Kind: EventError, Err: fmt.Errorf("pairing failed: %w", evt.Error)})
			return

		default:
			// Every other "err-" event is terminal (e.g. err-unexpected-state).
			// Reporting it as informational would leave the caller waiting on a
			// pairing that can never complete.
			if len(evt.Event) > 4 && evt.Event[:4] == "err-" {
				if evt.Error != nil {
					s.emit(ctx, Event{Kind: EventError, Err: fmt.Errorf("pairing failed (%s): %w", evt.Event, evt.Error)})
				} else {
					s.emit(ctx, Event{Kind: EventError, Err: fmt.Errorf("pairing failed (%s)", evt.Event)})
				}
				return
			}
		}
	}

	// The channel closed with no terminal event: the context was cancelled or
	// the socket dropped mid-pairing.
	if ctx.Err() != nil {
		s.emit(context.Background(), Event{Kind: EventError, Err: ctx.Err()})
		return
	}
	s.emit(context.Background(), Event{Kind: EventError, Err: fmt.Errorf("pairing ended without completing")})
}

// emit delivers an event unless the caller has gone away.
func (s *Session) emit(ctx context.Context, e Event) {
	select {
	case s.events <- e:
	case <-ctx.Done():
	}
}

// Events returns the pairing event stream. It closes when the flow reaches a
// terminal state.
func (s *Session) Events() <-chan Event { return s.events }

// SessionPath is where the pairing is persisted.
func (s *Session) SessionPath() string { return s.sessionPath }

// Close tears down the socket and the session store. Safe to call repeatedly.
func (s *Session) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.cancel()
		if s.client != nil {
			s.client.Disconnect()
		}
		if s.container != nil {
			err = s.container.Close()
		}
	})
	return err
}

// RenderQR writes code as a scannable QR block.
//
// Half-block rendering keeps the code square in a terminal whose cells are
// taller than they are wide; a full-block render is stretched and many phones
// fail to lock onto it. QuietZone is 1 rather than qrterminal's default 4 —
// the standard 4-module margin costs 6 rows and 6 columns, which is the
// difference between fitting and not fitting in a typical 80x30 terminal.
// Phones scan reliably at 1 against a contrasting terminal background.
func RenderQR(code string, w io.Writer) {
	qrterminal.GenerateWithConfig(code, qrterminal.Config{
		Level:          qrterminal.L,
		Writer:         w,
		HalfBlocks:     true,
		BlackChar:      qrterminal.BLACK_BLACK,
		WhiteChar:      qrterminal.WHITE_WHITE,
		BlackWhiteChar: qrterminal.BLACK_WHITE,
		WhiteBlackChar: qrterminal.WHITE_BLACK,
		QuietZone:      1,
	})
}

// QRDimensions reports the rows and columns RenderQR needs for code, so a
// caller can warn before drawing something the terminal will wrap into
// unscannable noise.
func QRDimensions(code string) (rows, cols int) {
	var c countingWriter
	RenderQR(code, &c)
	return c.rows, c.cols
}

// countingWriter measures rendered output without retaining it.
type countingWriter struct {
	rows int
	cols int
	cur  int
}

func (c *countingWriter) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			if c.cur > c.cols {
				c.cols = c.cur
			}
			c.cur = 0
			c.rows++
			continue
		}
		// Count runes, not bytes — the block glyphs are multi-byte.
		if b&0xC0 != 0x80 {
			c.cur++
		}
	}
	return len(p), nil
}

// pairCompletionTimeout bounds the post-scan handshake. The reconnect is
// usually sub-second; this only guards against a server that never completes.
const pairCompletionTimeout = 90 * time.Second

// pairSettleDelay is how long the socket is held open after the post-pair
// login lands.
//
// whatsmeow uploads prekeys and kicks off app-state sync asynchronously once
// logged in. Tearing the connection down the instant Connected fires can cut
// that short, leaving a device the server considers half-registered.
const pairSettleDelay = 4 * time.Second

// awaitPairCompletion blocks until the pairing is actually usable, and returns
// the Event to report.
//
// PairSuccess (the QR channel's "success") is NOT the end of pairing. At that
// point whatsmeow has written the device locally, but the server then drops
// the socket; the client reconnects and completes a login handshake, and only
// after that is the device registered server-side.
//
// Disconnecting on PairSuccess is what produced the field failure this guards
// against: pairing appeared to succeed, and the very next `forge run` was
// dropped with 401 "logged out from another device", because the registration
// was never finished.
func (s *Session) awaitPairCompletion(ctx context.Context) Event {
	return awaitCompletion(ctx, s.connected, s.fatal, s.client.IsLoggedIn, func() string {
		if s.client.Store.ID != nil {
			return s.client.Store.ID.ToNonAD().String()
		}
		return ""
	}, pairSettleDelay, pairCompletionTimeout)
}

// awaitCompletion is the socket-free core of awaitPairCompletion, so the
// completion rules can be tested without a live WhatsApp connection.
func awaitCompletion(
	ctx context.Context,
	connected <-chan struct{},
	fatal <-chan error,
	isLoggedIn func() bool,
	jid func() string,
	settle, timeout time.Duration,
) Event {
	select {
	case <-connected:
		// Logged in post-pair. Hold the socket briefly so the asynchronous
		// prekey upload and app-state sync can finish.
		select {
		case err := <-fatal:
			return Event{Kind: EventError, Err: err}
		case <-time.After(settle):
		case <-ctx.Done():
			return Event{Kind: EventError, Err: ctx.Err()}
		}
		if !isLoggedIn() {
			return Event{Kind: EventError, Err: fmt.Errorf("pairing did not complete: the server did not confirm the new device")}
		}
		return Event{Kind: EventPaired, JID: jid()}

	case err := <-fatal:
		return Event{Kind: EventError, Err: err}

	case <-time.After(timeout):
		return Event{Kind: EventError, Err: fmt.Errorf("timed out waiting for WhatsApp to confirm the new device")}

	case <-ctx.Done():
		return Event{Kind: EventError, Err: ctx.Err()}
	}
}
