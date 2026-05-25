package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/runtime"
	"github.com/initializ/forge/forge-core/types"
)

// TestB5_NotificationError_SurfacedAsInitialize proves the review-B5
// fix: when the initialized notification fails, the audit event
// records phase=initialize (not "discover" — the previous bug,
// where the silently-swallowed notification let tools/list be
// blamed for the real cause).
func TestB5_NotificationError_SurfacedAsInitialize(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg JSONRPCMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		w.Header().Set("Content-Type", "application/json")
		switch msg.Method {
		case MethodInitialize:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"protocolVersion":"` + ProtocolVersion + `","serverInfo":{"name":"m","version":"1"}}}`))
		case MethodInitialized:
			http.Error(w, "notification rejected", http.StatusBadGateway)
		case MethodToolsList:
			// Neutral text — must NOT contain the word "initialize"
			// (would mask the test if the classifier silently
			// regressed to substring matching).
			http.Error(w, "precondition failed: handshake incomplete", http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	var buf threadSafeAuditBuf
	audit := runtime.NewAuditLogger(&buf)
	s, err := NewServer(types.MCPServer{
		Name: "b5-notify", Transport: "http", URL: srv.URL,
		Tools: types.MCPToolFilter{Allow: []string{"x"}},
	}, ServerDeps{HTTPClient: srv.Client(), Audit: audit})
	if err != nil {
		t.Fatal(err)
	}
	s.backoff = []time.Duration{5 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = s.Run(ctx)

	log := buf.String()
	if !strings.Contains(log, `"phase":"initialize"`) {
		t.Errorf("expected phase=initialize in failure audit, got:\n%s", log)
	}
	if strings.Contains(log, `"phase":"discover"`) {
		t.Errorf("phase=discover leaked — notification error masked as discover failure:\n%s", log)
	}
}

// TestB5_ToolsListError_ReportedAsDiscover sanity-checks the other
// side of the classifier: when the notification succeeds and
// tools/list fails, phase MUST be discover. Pins both directions.
func TestB5_ToolsListError_ReportedAsDiscover(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg JSONRPCMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		w.Header().Set("Content-Type", "application/json")
		switch msg.Method {
		case MethodInitialize:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"protocolVersion":"` + ProtocolVersion + `","serverInfo":{"name":"m","version":"1"}}}`))
		case MethodInitialized:
			w.WriteHeader(http.StatusAccepted)
		case MethodToolsList:
			http.Error(w, "tools subsystem unavailable", http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()

	var buf threadSafeAuditBuf
	audit := runtime.NewAuditLogger(&buf)
	s, err := NewServer(types.MCPServer{
		Name: "b5-discover", Transport: "http", URL: srv.URL,
		Tools: types.MCPToolFilter{Allow: []string{"x"}},
	}, ServerDeps{HTTPClient: srv.Client(), Audit: audit})
	if err != nil {
		t.Fatal(err)
	}
	s.backoff = []time.Duration{5 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = s.Run(ctx)

	log := buf.String()
	if !strings.Contains(log, `"phase":"discover"`) {
		t.Errorf("expected phase=discover in failure audit, got:\n%s", log)
	}
	if strings.Contains(log, `"phase":"initialize"`) {
		t.Errorf("phase=initialize leaked — tools/list error misclassified:\n%s", log)
	}
}

// TestB5_PhaseClassifier_PrefixBased pins the deterministic
// classifier directly. The old substring-based classifier returned
// "initialize" for ANY error containing that word. The new
// prefix-based one requires the wrap prefix our own runOnce emits.
func TestB5_PhaseClassifier_PrefixBased(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, "unknown"},
		{"connect prefix", errors.New("connect: dial tcp: refused"), "connect"},
		{"initialize prefix", errors.New("initialize: HTTP 502"), "initialize"},
		{"initialized notification prefix", errors.New("initialized notification: HTTP 502"), "initialize"},
		{"tools/list prefix", errors.New("tools/list: HTTP 503"), "discover"},
		{"tool[N] prefix (schema validation)", errors.New(`tool[0] "echo": malformed JSON Schema`), "discover"},
		{"unrelated text containing 'initialize'", errors.New("server says: not yet initialized properly"), "runtime"},
		{"unrelated text containing 'tools/list'", errors.New("ignore me: tools/list mentioned elsewhere"), "runtime"},
		{"random transport error", errors.New("transport: connection reset"), "runtime"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyFailurePhase(tc.err); got != tc.want {
				t.Errorf("classifyFailurePhase(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestB5_NotificationTimeout_AlsoSurfaced — a hanging notification
// endpoint must time out within Spec.Timeout and propagate, not
// hang the lifecycle forever.
func TestB5_NotificationTimeout_AlsoSurfaced(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg JSONRPCMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		w.Header().Set("Content-Type", "application/json")
		switch msg.Method {
		case MethodInitialize:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + msg.ID.String() + `,"result":{"protocolVersion":"` + ProtocolVersion + `","serverInfo":{"name":"m","version":"1"}}}`))
		case MethodInitialized:
			// Hang up to 5s (bounded for clean teardown).
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		}
	}))
	defer srv.Close()

	var buf threadSafeAuditBuf
	audit := runtime.NewAuditLogger(&buf)
	s, err := NewServer(types.MCPServer{
		Name: "b5-hang", Transport: "http", URL: srv.URL,
		Tools:   types.MCPToolFilter{Allow: []string{"x"}},
		Timeout: 100 * time.Millisecond,
	}, ServerDeps{HTTPClient: srv.Client(), Audit: audit})
	if err != nil {
		t.Fatal(err)
	}
	s.backoff = []time.Duration{5 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.Run(ctx)

	log := buf.String()
	if !strings.Contains(log, `"phase":"initialize"`) {
		t.Errorf("expected phase=initialize for notification timeout:\n%s", log)
	}
}
