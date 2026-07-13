package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	gort "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// shortSocketPath returns a path short enough for the macOS UDS limit
// (~104 bytes). `t.TempDir()` on darwin lands under /var/folders/...
// which blows past it; /tmp keeps us safe. Auto-cleaned via t.Cleanup.
func shortSocketPath(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "f7")
	if err != nil {
		t.Fatalf("mkdir tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, name)
}

// Coverage for the FWS-7 sink layer (issue #95). The 11 numbered tests
// follow the test plan in the issue. Tests use stdlib only — no
// testify, matching the project's testing conventions.

// 1. writerSink writes the exact bytes it receives.
func TestWriterSink_WritesExactBytes(t *testing.T) {
	var buf bytes.Buffer
	s := newWriterSink(&buf, "test")
	want := []byte(`{"event":"x"}` + "\n")
	if err := s.Write(context.Background(), want); err != nil {
		t.Fatalf("Write returned err: %v", err)
	}
	if got := buf.Bytes(); !bytes.Equal(got, want) {
		t.Errorf("buf = %q, want %q", got, want)
	}
	if s.Stats()["writes_ok"] != 1 {
		t.Errorf("writes_ok = %d, want 1", s.Stats()["writes_ok"])
	}
}

// 2. socketSink delivers to a listening UDS.
func TestSocketSink_DialSuccess_EventArrives(t *testing.T) {
	if runtimeGOOS_isWindows() {
		t.Skip("unix socket sink not exercised on Windows")
	}
	sockPath := shortSocketPath(t, "audit.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	got := make(chan []byte, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		r := bufio.NewReader(c)
		line, _ := r.ReadBytes('\n')
		got <- line
	}()

	s := NewSocketSink(sockPath, 200*time.Millisecond, 500*time.Millisecond)
	defer func() { _ = s.Close(context.Background()) }()
	want := []byte(`{"event":"hello"}` + "\n")
	if err := s.Write(context.Background(), want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	select {
	case line := <-got:
		if !bytes.Equal(line, want) {
			t.Errorf("listener got %q, want %q", line, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("listener never received the event")
	}
	if s.Stats()["writes_ok"] != 1 {
		t.Errorf("writes_ok = %d, want 1", s.Stats()["writes_ok"])
	}
}

// 3. socketSink with a non-existent path increments drops_dial and
// returns nil — never blocks the emitter.
func TestSocketSink_DialFailure_DropsAndReturnsNil(t *testing.T) {
	if runtimeGOOS_isWindows() {
		t.Skip("unix socket sink not exercised on Windows")
	}
	sockPath := shortSocketPath(t, "nope.sock")
	s := NewSocketSink(sockPath, 200*time.Millisecond, 100*time.Millisecond)
	defer func() { _ = s.Close(context.Background()) }()
	if err := s.Write(context.Background(), []byte(`{}`+"\n")); err != nil {
		t.Errorf("Write should return nil on dial failure, got %v", err)
	}
	if s.Stats()["drops_dial"] != 1 {
		t.Errorf("drops_dial = %d, want 1", s.Stats()["drops_dial"])
	}
	if s.Stats()["writes_ok"] != 0 {
		t.Errorf("writes_ok should stay 0, got %d", s.Stats()["writes_ok"])
	}
}

// 4. socketSink reconnects after the listener closes the connection
// from under it (simulates sidecar restart).
func TestSocketSink_ReconnectsAfterPeerClose(t *testing.T) {
	if runtimeGOOS_isWindows() {
		t.Skip("unix socket sink not exercised on Windows")
	}
	sockPath := shortSocketPath(t, "audit.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	// First accept: read one line then close to simulate peer drop.
	firstAccept := make(chan struct{})
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		_, _ = bufio.NewReader(c).ReadBytes('\n')
		_ = c.Close()
		close(firstAccept)
	}()

	s := NewSocketSink(sockPath, 200*time.Millisecond, 500*time.Millisecond)
	defer func() { _ = s.Close(context.Background()) }()
	if err := s.Write(context.Background(), []byte(`{"i":1}`+"\n")); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	<-firstAccept

	// Second accept: confirm the sink re-dials. We need to skip past
	// the backoff window the sink imposed after the peer close.
	gotSecond := make(chan []byte, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		line, _ := bufio.NewReader(c).ReadBytes('\n')
		gotSecond <- line
	}()

	// Sink uses 100ms initial backoff doubled to 200ms after the
	// first failure. Sleep past that, then try again.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_ = s.Write(context.Background(), []byte(`{"i":2}`+"\n"))
		select {
		case line := <-gotSecond:
			if !strings.Contains(string(line), `"i":2`) {
				t.Errorf("re-connected listener got %q", line)
			}
			_ = ln.Close()
			return
		default:
			time.Sleep(60 * time.Millisecond)
		}
	}
	_ = ln.Close()
	t.Fatal("sink never reconnected after peer close")
}

// 5. socketSink whose peer accepts but never reads → write times out;
// counter increments; connection is dropped for next attempt.
func TestSocketSink_WriteTimeout_DropsAndDisconnects(t *testing.T) {
	if runtimeGOOS_isWindows() {
		t.Skip("unix socket sink not exercised on Windows")
	}
	sockPath := shortSocketPath(t, "audit.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		c, _ := ln.Accept()
		if c != nil {
			// Hold the conn open without reading — fills kernel buffer.
			time.Sleep(2 * time.Second)
			_ = c.Close()
		}
	}()

	s := NewSocketSink(sockPath, 30*time.Millisecond, 500*time.Millisecond).(*socketSink)
	defer func() { _ = s.Close(context.Background()) }()

	// One write is likely to succeed (small payload fits in kernel
	// buffer). Pump a moderately large payload until at least one
	// drop is recorded.
	payload := append(bytes.Repeat([]byte("x"), 64<<10), '\n')
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_ = s.Write(context.Background(), payload)
		if s.Stats()["drops_timeout"] > 0 {
			break
		}
	}
	if s.Stats()["drops_timeout"] == 0 {
		t.Errorf("expected at least one drops_timeout under a non-reading peer, got stats=%v", s.Stats())
	}
}

// 6. httpSink POSTs to the configured endpoint with the event bytes
// as the request body.
func TestHTTPSink_Posts_Event(t *testing.T) {
	gotBody := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody <- body
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	s := NewHTTPSink(srv.URL, 500*time.Millisecond)
	defer func() { _ = s.Close(context.Background()) }()
	want := []byte(`{"event":"http"}` + "\n")
	if err := s.Write(context.Background(), want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	select {
	case body := <-gotBody:
		if !bytes.Equal(body, want) {
			t.Errorf("server got %q, want %q", body, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server never received the request")
	}
	if s.Stats()["writes_ok"] != 1 {
		t.Errorf("writes_ok = %d, want 1", s.Stats()["writes_ok"])
	}
}

// 6b. httpSink health is a live level: a 2xx sets connected=1 and every
// failure path clears it to 0, so the #280 connected-flip edge fires on an
// HTTP endpoint outage (previously connected stayed sticky at its last
// success and an outage was invisible until the next keepalive).
func TestHTTPSink_ErrorPathsClearConnected(t *testing.T) {
	var status atomic.Int64
	status.Store(http.StatusAccepted)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(status.Load()))
	}))

	s := NewHTTPSink(srv.URL, 200*time.Millisecond)
	defer func() { _ = s.Close(context.Background()) }()
	write := func() {
		if err := s.Write(context.Background(), []byte(`{}`+"\n")); err != nil {
			t.Fatalf("Write returned err: %v", err)
		}
	}

	// 2xx → healthy.
	write()
	if s.Stats()["connected"] != 1 {
		t.Fatalf("after a 2xx, connected = %d, want 1", s.Stats()["connected"])
	}

	// non-2xx → the outage must flip the edge to 0, not stay stuck at 1.
	status.Store(http.StatusInternalServerError)
	write()
	if s.Stats()["connected"] != 0 {
		t.Errorf("after a non-2xx, connected = %d, want 0", s.Stats()["connected"])
	}

	// recovery → back to 1 (the 0→1 edge the heartbeat reports as recovery).
	status.Store(http.StatusAccepted)
	write()
	if s.Stats()["connected"] != 1 {
		t.Errorf("after recovery, connected = %d, want 1", s.Stats()["connected"])
	}

	// transport error (endpoint gone) → 0, mirroring the socket sink.
	srv.Close()
	write()
	if s.Stats()["connected"] != 0 {
		t.Errorf("after a transport error, connected = %d, want 0", s.Stats()["connected"])
	}
}

// 7. Multi-sink fan-out: stderr + socket configured; one Emit reaches
// both sinks.
func TestAuditLogger_FanOut_AllSinksReceive(t *testing.T) {
	if runtimeGOOS_isWindows() {
		t.Skip("unix socket sink not exercised on Windows")
	}
	sockPath := shortSocketPath(t, "audit.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	sockGot := make(chan []byte, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		line, _ := bufio.NewReader(c).ReadBytes('\n')
		sockGot <- line
	}()

	var bufSink bytes.Buffer
	logger := &AuditLogger{
		sinks: []Sink{
			newWriterSink(&bufSink, "buf"),
			NewSocketSink(sockPath, 500*time.Millisecond, 500*time.Millisecond),
		},
		logOnce: map[string]bool{},
	}

	logger.Emit(AuditEvent{Event: "x", Fields: map[string]any{"k": "v"}})

	select {
	case line := <-sockGot:
		if !strings.Contains(string(line), `"event":"x"`) {
			t.Errorf("socket sink got %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("socket sink did not receive the fan-out event")
	}
	if !strings.Contains(bufSink.String(), `"event":"x"`) {
		t.Errorf("buf sink missing event, got: %q", bufSink.String())
	}
}

// 8. One sink failure does not stop the others. Socket sink pointed
// at a missing path; the stderr/buf sink still receives the event.
func TestAuditLogger_FanOut_BadSinkDoesNotStopOthers(t *testing.T) {
	var bufSink bytes.Buffer
	logger := &AuditLogger{
		sinks: []Sink{
			newWriterSink(&bufSink, "buf"),
			NewSocketSink(shortSocketPath(t, "absent.sock"), 100*time.Millisecond, 100*time.Millisecond),
		},
		logOnce: map[string]bool{},
	}
	logger.Emit(AuditEvent{Event: "still-here"})
	if !strings.Contains(bufSink.String(), `"event":"still-here"`) {
		t.Errorf("buf sink missing event despite sibling-sink failure: %q", bufSink.String())
	}
}

// 9. Stats reflect what happened: N successes, M dial-drops.
func TestSocketSink_Stats_Accurate(t *testing.T) {
	if runtimeGOOS_isWindows() {
		t.Skip("unix socket sink not exercised on Windows")
	}
	// Drops only — no listener.
	s := NewSocketSink(shortSocketPath(t, "nope2.sock"), 50*time.Millisecond, 50*time.Millisecond)
	defer func() { _ = s.Close(context.Background()) }()
	for i := 0; i < 3; i++ {
		_ = s.Write(context.Background(), []byte("{}\n"))
	}
	stats := s.Stats()
	if stats["drops_dial"] < 1 {
		t.Errorf("expected at least 1 drop_dial across 3 writes (backoff suppresses some), got %d", stats["drops_dial"])
	}
	if stats["writes_ok"] != 0 {
		t.Errorf("writes_ok should be 0, got %d", stats["writes_ok"])
	}
}

// 10. Concurrent emission: 100 goroutines × 10 emits each → 1000 events
// land on the configured listener.
func TestAuditLogger_ConcurrentEmission(t *testing.T) {
	t.Parallel()
	if runtimeGOOS_isWindows() {
		t.Skip("unix socket sink not exercised on Windows")
	}
	sockPath := shortSocketPath(t, "audit.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	// Concurrency exercise: 20 goroutines × 5 events = 100 events
	// total. The sink serializes through one connection so larger
	// counts buy nothing — the assertion is "no drops, no
	// interleaved bytes," which 100 events under 20-way contention
	// covers cleanly.
	const N, M = 20, 5
	const expected = N * M

	var received atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		r := bufio.NewReader(c)
		// Read exactly the expected count then return so the
		// listener goroutine doesn't hang on ReadBytes after the
		// last write (ln.Close on the parent listener doesn't
		// close the already-accepted conn).
		for i := 0; i < expected; i++ {
			_, err := r.ReadBytes('\n')
			if err != nil {
				return
			}
			received.Add(1)
		}
	}()

	logger := &AuditLogger{
		sinks: []Sink{
			NewSocketSink(sockPath, 500*time.Millisecond, 500*time.Millisecond),
		},
		logOnce: map[string]bool{},
	}
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			for j := 0; j < M; j++ {
				logger.Emit(AuditEvent{Event: "c", Fields: map[string]any{"g": i, "j": j}})
			}
		}(i)
	}
	wg.Wait()

	// Drain a moment so the listener catches up.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && received.Load() < N*M {
		time.Sleep(20 * time.Millisecond)
	}
	if got := received.Load(); got != N*M {
		t.Errorf("listener received %d events, want %d", got, N*M)
	}
	_ = ln.Close()
	<-done
}

// 11. Close drains pending writes and refuses subsequent ones.
func TestSocketSink_CloseRefusesNewWrites(t *testing.T) {
	if runtimeGOOS_isWindows() {
		t.Skip("unix socket sink not exercised on Windows")
	}
	sockPath := shortSocketPath(t, "audit.sock")
	ln, _ := net.Listen("unix", sockPath)
	defer func() { _ = ln.Close() }()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			defer func() { _ = c.Close() }()
			_, _ = io.Copy(io.Discard, c)
		}
	}()

	s := NewSocketSink(sockPath, 200*time.Millisecond, 500*time.Millisecond)
	if err := s.Write(context.Background(), []byte("{}\n")); err != nil {
		t.Fatalf("pre-close Write: %v", err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Write(context.Background(), []byte("{}\n")); err == nil {
		t.Errorf("post-close Write should return an error")
	}
}

// AuditExportConfigFromEnv reads FORGE_AUDIT_* vars; flag-side wiring
// (CLI) is exercised in the integration test.
func TestAuditExportConfigFromEnv(t *testing.T) {
	t.Setenv(EnvAuditSocket, "/tmp/forge.sock")
	t.Setenv(EnvAuditHTTPEndpoint, "http://127.0.0.1:9097/v1/audit")
	t.Setenv(EnvAuditWriteTimeout, "75ms")
	cfg := AuditExportConfigFromEnv()
	if cfg.SocketPath != "/tmp/forge.sock" {
		t.Errorf("SocketPath = %q", cfg.SocketPath)
	}
	if cfg.HTTPEndpoint != "http://127.0.0.1:9097/v1/audit" {
		t.Errorf("HTTPEndpoint = %q", cfg.HTTPEndpoint)
	}
	if cfg.WriteTimeout != 75*time.Millisecond {
		t.Errorf("WriteTimeout = %v", cfg.WriteTimeout)
	}
}

// NewAuditLoggerFromConfig with an empty config behaves like the
// legacy NewAuditLogger(os.Stderr) — one sink, stderr.
func TestNewAuditLoggerFromConfig_EmptyIsStderrOnly(t *testing.T) {
	logger := NewAuditLoggerFromConfig(AuditExportConfig{})
	sinks := logger.Sinks()
	if len(sinks) != 1 {
		t.Fatalf("len(sinks) = %d, want 1", len(sinks))
	}
	if sinks[0].Name() != "stderr" {
		t.Errorf("sinks[0].Name() = %q, want stderr", sinks[0].Name())
	}
}

// audit_export_status fires on every tick; one event per registered
// sink in the fields.sinks array.
// collectStatusEvents drains a buffer of NDJSON and returns the
// audit_export_status events.
func collectStatusEvents(t *testing.T, s string) []AuditEvent {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	var out []AuditEvent
	for dec.More() {
		var evt AuditEvent
		if err := dec.Decode(&evt); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if evt.Event == AuditExportStatus {
			out = append(out, evt)
		}
	}
	return out
}

// TestStartAuditExportStatus_EmitsPerSinkReport pins the baseline shape: an
// initial keepalive is emitted at startup carrying one sinks[] entry per sink
// with a reason tag (#280).
func TestStartAuditExportStatus_EmitsPerSinkReport(t *testing.T) {
	var buf bytes.Buffer
	logger := &AuditLogger{
		sinks:   []Sink{newWriterSink(&buf, "buf-status")},
		logOnce: map[string]bool{},
	}
	stop := StartAuditExportStatus(context.Background(), logger)
	time.Sleep(30 * time.Millisecond) // enough for the startup emit
	stop()

	evts := collectStatusEvents(t, buf.String())
	if len(evts) < 1 {
		t.Fatalf("expected at least the startup keepalive, got %d", len(evts))
	}
	e := evts[0]
	if e.Fields["reason"] != auditStatusReasonKeepalive {
		t.Errorf("startup emit reason = %v, want keepalive", e.Fields["reason"])
	}
	sinks, ok := e.Fields["sinks"].([]any)
	if !ok || len(sinks) != 1 {
		t.Errorf("status event fields.sinks = %v", e.Fields["sinks"])
	}
}

// TestStartAuditExportStatus_SlowKeepalive pins the #280 volume fix: with a
// healthy (unchanging) sink, a LONG keepalive means only the startup emit
// appears — no per-poll heartbeat spam.
func TestStartAuditExportStatus_SlowKeepalive(t *testing.T) {
	prevPoll := AuditExportStatusPollInterval
	prevKA := AuditExportStatusKeepaliveInterval
	AuditExportStatusPollInterval = 5 * time.Millisecond
	AuditExportStatusKeepaliveInterval = time.Hour // effectively never in this test
	defer func() {
		AuditExportStatusPollInterval = prevPoll
		AuditExportStatusKeepaliveInterval = prevKA
	}()

	var buf bytes.Buffer
	logger := &AuditLogger{sinks: []Sink{newWriterSink(&buf, "buf-status")}, logOnce: map[string]bool{}}
	stop := StartAuditExportStatus(context.Background(), logger)
	time.Sleep(60 * time.Millisecond) // ~12 polls, no state change
	stop()

	if n := len(collectStatusEvents(t, buf.String())); n != 1 {
		t.Errorf("healthy steady state should emit only the startup keepalive; got %d events", n)
	}
}

// TestStartAuditExportStatus_EmitsOnConnectedFlip pins the integrity half: a
// sink whose `connected` flag flips triggers an immediate state_change emit
// even under a long keepalive. `connected` is the edge signal (#280 review) —
// both a 1→0 failure and a 0→1 recovery must fire.
func TestStartAuditExportStatus_EmitsOnConnectedFlip(t *testing.T) {
	prevPoll := AuditExportStatusPollInterval
	prevKA := AuditExportStatusKeepaliveInterval
	AuditExportStatusPollInterval = 5 * time.Millisecond
	AuditExportStatusKeepaliveInterval = time.Hour
	defer func() {
		AuditExportStatusPollInterval = prevPoll
		AuditExportStatusKeepaliveInterval = prevKA
	}()

	var buf bytes.Buffer
	sink := newFakeStatSink("flaky") // starts connected
	// The fakeStatSink drives the state change; a writer sink captures the
	// emitted status events into buf (fakeStatSink discards writes).
	logger := &AuditLogger{sinks: []Sink{sink, newWriterSink(&buf, "buf")}, logOnce: map[string]bool{}}
	stop := StartAuditExportStatus(context.Background(), logger)
	time.Sleep(20 * time.Millisecond) // startup emit + a few clean polls
	sink.setConnected(0)              // fail: 1 -> 0
	time.Sleep(20 * time.Millisecond) // a poll observes the failure
	sink.setConnected(1)              // recover: 0 -> 1
	time.Sleep(20 * time.Millisecond) // a poll observes the recovery
	stop()

	reasons := statusReasons(collectStatusEvents(t, buf.String()))
	var stateChanges int
	for _, r := range reasons {
		if r == auditStatusReasonStateChange {
			stateChanges++
		}
	}
	if stateChanges < 2 {
		t.Errorf("both the 1->0 failure and 0->1 recovery must emit state_change; got %d in %v", stateChanges, reasons)
	}
}

// TestStartAuditExportStatus_PersistentOutageDoesNotAmplify is the #280-review
// regression guard: during a SUSTAINED outage the status event must not
// self-amplify. Because every emit writes to the failing sink (bumping its
// drop counter), a drop-delta edge would re-fire every poll — one event per
// poll for the whole outage. With `connected` as the edge, a down sink settles
// after a single 1→0 transition, so the emit count stays bounded no matter how
// many poll windows elapse.
func TestStartAuditExportStatus_PersistentOutageDoesNotAmplify(t *testing.T) {
	prevPoll := AuditExportStatusPollInterval
	prevKA := AuditExportStatusKeepaliveInterval
	AuditExportStatusPollInterval = 2 * time.Millisecond
	AuditExportStatusKeepaliveInterval = time.Hour
	defer func() {
		AuditExportStatusPollInterval = prevPoll
		AuditExportStatusKeepaliveInterval = prevKA
	}()

	var buf bytes.Buffer
	sink := newFakeStatSink("down")
	sink.downOnWrite = true // first write drops the connection; every write bumps drops
	logger := &AuditLogger{sinks: []Sink{sink, newWriterSink(&buf, "buf")}, logOnce: map[string]bool{}}
	stop := StartAuditExportStatus(context.Background(), logger)
	time.Sleep(80 * time.Millisecond) // ~40 poll windows of continuous dropping
	stop()

	// A drop-delta edge would emit ~40 times here; `connected` gives exactly
	// the startup keepalive + one 1→0 state_change. Bound generously to stay
	// robust against scheduler jitter while still catching amplification.
	evts := collectStatusEvents(t, buf.String())
	if len(evts) > 3 {
		t.Errorf("sustained outage self-amplified: %d status events across ~40 polls (reasons %v)", len(evts), statusReasons(evts))
	}
	var stateChanges int
	for _, e := range evts {
		if e.Fields["reason"] == auditStatusReasonStateChange {
			stateChanges++
		}
	}
	if stateChanges != 1 {
		t.Errorf("a settled outage must produce exactly one state_change; got %d in %v", stateChanges, statusReasons(evts))
	}
}

func statusReasons(evts []AuditEvent) []any {
	out := make([]any, 0, len(evts))
	for _, e := range evts {
		out = append(out, e.Fields["reason"])
	}
	return out
}

// fakeStatSink is a Sink whose health we can mutate to simulate transitions.
// Writes are discarded. `connected` starts at 1 (use newFakeStatSink); tests
// flip it via setConnected. When downOnWrite is set, every write drops the
// connection and bumps the drop counter — this reproduces the outage
// self-feed the status heartbeat must not amplify (#280 review).
type fakeStatSink struct {
	name        string
	connected   atomic.Int64
	drops       atomic.Int64
	downOnWrite bool
}

func newFakeStatSink(name string) *fakeStatSink {
	f := &fakeStatSink{name: name}
	f.connected.Store(1)
	return f
}

func (f *fakeStatSink) Name() string { return f.name }
func (f *fakeStatSink) Write(context.Context, []byte) error {
	if f.downOnWrite {
		f.connected.Store(0)
		f.drops.Add(1)
	}
	return nil
}
func (f *fakeStatSink) Close(context.Context) error { return nil }
func (f *fakeStatSink) setConnected(v int64)        { f.connected.Store(v) }
func (f *fakeStatSink) Stats() map[string]int64 {
	return map[string]int64{
		"connected":     f.connected.Load(),
		"drops_dial":    f.drops.Load(),
		"drops_timeout": 0,
		"writes_ok":     0,
	}
}

// runtimeGOOS_isWindows is the local equivalent of runtime.GOOS ==
// "windows". Uses the gort alias so this file can sit in package
// runtime without name collisions. UDS tests skip on Windows.
func runtimeGOOS_isWindows() bool { return gort.GOOS == "windows" }

// TestResolveKeepaliveInterval_EnvOverride pins the AUDIT_STATUS_KEEPALIVE_INTERVAL
// override and the fall-back-on-garbage behavior (#280).
func TestResolveKeepaliveInterval_EnvOverride(t *testing.T) {
	prev := AuditExportStatusKeepaliveInterval
	AuditExportStatusKeepaliveInterval = 15 * time.Minute
	defer func() { AuditExportStatusKeepaliveInterval = prev }()

	t.Setenv(EnvAuditStatusKeepaliveInterval, "3m")
	if got := resolveKeepaliveInterval(); got != 3*time.Minute {
		t.Errorf("env override = %v, want 3m", got)
	}
	t.Setenv(EnvAuditStatusKeepaliveInterval, "not-a-duration")
	if got := resolveKeepaliveInterval(); got != AuditExportStatusKeepaliveInterval {
		t.Errorf("invalid env should fall back to the package default; got %v", got)
	}
}
