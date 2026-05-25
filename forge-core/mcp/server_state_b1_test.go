package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/types"
)

// recordingLogger captures messages keyed by level so tests can
// assert (a) no "illegal state transition" messages appear and
// (b) audit / warning content as needed.
type recordingLogger struct {
	mu   sync.Mutex
	msgs []string
}

func (l *recordingLogger) record(level, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.msgs = append(l.msgs, level+": "+msg)
}
func (l *recordingLogger) Info(msg string, _ map[string]any)  { l.record("INFO", msg) }
func (l *recordingLogger) Warn(msg string, _ map[string]any)  { l.record("WARN", msg) }
func (l *recordingLogger) Error(msg string, _ map[string]any) { l.record("ERROR", msg) }
func (l *recordingLogger) Snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string{}, l.msgs...)
}

// TestB1_FailurePaths_NoIllegalTransitions proves the review-B1 fix.
//
// Each failure path (connect refused, initialize 503, malformed
// schema, demux exit after Ready) used to silently log "illegal
// state transition" for every retry attempt while leaving the
// state field stuck at e.g. Initializing. After the fix:
//
//   - Run completes without ever calling Logger.Error("illegal ...").
//   - transition() in test mode would panic on any illegal move —
//     this test relies on go test's default behavior, so a regression
//     here would surface as a test panic, not silent acceptance.
func TestB1_FailurePaths_NoIllegalTransitions(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "initialize_503",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "down", http.StatusServiceUnavailable)
			},
		},
		{
			name: "initialize_4xx_protocol_error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "bad request", http.StatusBadRequest)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()

			rec := &recordingLogger{}
			s, err := NewServer(types.MCPServer{
				Name: "b1-" + tc.name, Transport: "http", URL: srv.URL,
				Tools:    types.MCPToolFilter{Allow: []string{"x"}},
				Required: false,
			}, ServerDeps{HTTPClient: srv.Client(), Logger: rec})
			if err != nil {
				t.Fatal(err)
			}
			// Two short backoffs so the test finishes fast but still
			// exercises the retry loop.
			s.backoff = []time.Duration{5 * time.Millisecond, 5 * time.Millisecond}

			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			defer cancel()
			if err := s.Run(ctx); err != nil {
				t.Fatalf("Run returned %v (Required=false should return nil)", err)
			}

			// No "illegal" messages logged — proves every transition was
			// legal per the table.
			for _, m := range rec.Snapshot() {
				if strings.Contains(strings.ToLower(m), "illegal") {
					t.Errorf("illegal-transition message leaked through: %s", m)
				}
			}
			// State settled at Stopped (terminal).
			if got := s.State(); got != StateStopped {
				t.Errorf("final state = %s, want stopped", got)
			}
		})
	}
}

// TestB1_TransitionPanicsOnIllegal pins the fail-loud property —
// transition() must panic on illegal moves under `go test`. If a
// future refactor silently log-and-returns again, this test fails
// immediately.
func TestB1_TransitionPanicsOnIllegal(t *testing.T) {
	t.Parallel()
	s := &Server{
		Name:   "panic-test",
		state:  StateConfigured,
		logger: nopLogger{},
	}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic on Configured→Failed (illegal), got none")
		}
		if !strings.Contains(r.(string), "illegal mcp state transition") {
			t.Errorf("panic message lacks expected prefix: %v", r)
		}
	}()
	// Configured → Failed is NOT a legal transition (Configured can
	// only go to Connecting or Stopped). This MUST panic.
	s.transition(StateFailed)
}

// TestB1_StateProgressionThroughDegraded confirms the new model —
// every failure path emits the mcp_server_degraded audit event,
// which the Run loop only emits AFTER transitioning Degraded →
// Reconnecting (so its presence proves both legal transitions ran).
// Auditing instead of polling State() avoids racing the back-to-back
// transitions.
func TestB1_StateProgressionThroughDegraded(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	var buf threadSafeAuditBuf
	audit := runtime.NewAuditLogger(&buf)
	s, err := NewServer(types.MCPServer{
		Name: "progression", Transport: "http", URL: srv.URL,
		Tools:    types.MCPToolFilter{Allow: []string{"x"}},
		Required: false,
	}, ServerDeps{HTTPClient: srv.Client(), Audit: audit})
	if err != nil {
		t.Fatal(err)
	}
	s.backoff = []time.Duration{5 * time.Millisecond, 5 * time.Millisecond}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if err := s.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	log := buf.String()
	if !strings.Contains(log, "mcp_server_degraded") {
		t.Errorf("expected mcp_server_degraded audit event, got: %s", log)
	}
	if !strings.Contains(log, "mcp_server_failed") {
		t.Errorf("expected mcp_server_failed audit event after backoff exhaustion, got: %s", log)
	}
}

type threadSafeAuditBuf struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *threadSafeAuditBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *threadSafeAuditBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestB1_NewTransitions_AllRequiredEdgesLegal pins every edge the
// fix added — guards against accidental table edits.
func TestB1_NewTransitions_AllRequiredEdgesLegal(t *testing.T) {
	t.Parallel()
	required := []struct{ from, to ServerState }{
		{StateConnecting, StateDegraded},
		{StateInitializing, StateDegraded},
		{StateDiscovering, StateDegraded},
		{StateReady, StateDegraded},
		{StateCalling, StateDegraded},
		{StateDegraded, StateReconnecting},
		{StateDegraded, StateFailed},
		{StateReconnecting, StateConnecting},
		{StateReconnecting, StateFailed},
	}
	for _, r := range required {
		if !isValidTransition(r.from, r.to) {
			t.Errorf("%s → %s must be legal (review B1 fix)", r.from, r.to)
		}
	}

	// And these MUST stay illegal — they were the symptom in B1:
	illegal := []struct{ from, to ServerState }{
		{StateConnecting, StateReconnecting},   // must route via Degraded
		{StateInitializing, StateReconnecting}, // must route via Degraded
		{StateDiscovering, StateReconnecting},  // must route via Degraded
		{StateReady, StateReconnecting},        // must route via Degraded
		{StateInitializing, StateInitializing}, // self-loop nonsense
	}
	for _, r := range illegal {
		if isValidTransition(r.from, r.to) {
			t.Errorf("%s → %s must remain illegal (route via Degraded instead)", r.from, r.to)
		}
	}
}

// Ensure runtime is referenced so the import survives gofmt's
// unused-import check even if test refactoring trims direct uses.
var _ = runtime.GenerateID
