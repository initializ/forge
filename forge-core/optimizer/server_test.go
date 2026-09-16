package optimizer

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeUpstream stands in for the Anthropic API: it echoes a canned SSE stream
// or JSON body and records what it received, so tests can assert both
// pass-through fidelity and header forwarding.
// signalReporter closes done after the first Report, so tests can wait for the
// handler to finish metering (Report runs after the body is fully streamed,
// which races the client's ReadAll returning on EOF).
type signalReporter struct{ done chan struct{} }

func (s *signalReporter) Report(context.Context, Report) {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}

func newServerTo(t *testing.T, upstream *httptest.Server) (*Server, *signalReporter) {
	t.Helper()
	sig := &signalReporter{done: make(chan struct{})}
	s, err := New(Config{Listen: "127.0.0.1:0", Upstream: upstream.URL, Reporter: sig})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, sig
}

func waitReport(t *testing.T, sig *signalReporter) {
	t.Helper()
	select {
	case <-sig.done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for report")
	}
}

const sseBody = "event: message_start\n" +
	`data: {"type":"message_start","message":{"model":"claude-opus-4-8","usage":{"input_tokens":1200,"cache_read_input_tokens":800,"cache_creation_input_tokens":0,"output_tokens":1}}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hello"}}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":57}}` + "\n\n"

func TestProxy_StreamingPassthroughAndUsage(t *testing.T) {
	var gotAuth, gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, sseBody)
	}))
	defer upstream.Close()

	s, sig := newServerTo(t, upstream)
	front := httptest.NewServer(s.Handler())
	defer front.Close()

	req, _ := http.NewRequest(http.MethodPost, front.URL+"/v1/messages", strings.NewReader(`{"stream":true}`))
	req.Header.Set("Authorization", "Bearer sk-ant-test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	// Fidelity: the client receives the SSE stream byte-for-byte.
	if string(body) != sseBody {
		t.Errorf("body not passed through verbatim:\n got=%q", string(body))
	}
	// Header forwarding: the caller's auth reaches the upstream untouched.
	if gotAuth != "Bearer sk-ant-test" {
		t.Errorf("auth not forwarded, got %q", gotAuth)
	}
	if gotPath != "/v1/messages" {
		t.Errorf("path not preserved, got %q", gotPath)
	}

	// Usage: input+cache from message_start, final output from message_delta.
	waitReport(t, sig)
	snap := s.stats.snapshot()
	if snap.Totals.Requests != 1 {
		t.Fatalf("requests = %d, want 1", snap.Totals.Requests)
	}
	if snap.Totals.InputTokens != 1200 || snap.Totals.OutputTokens != 57 || snap.Totals.CacheReadInputTokens != 800 {
		t.Errorf("usage mismatch: %+v", snap.Totals)
	}
	if _, ok := snap.PerModel["claude-opus-4-8"]; !ok {
		t.Errorf("per-model missing model key: %+v", snap.PerModel)
	}
}

func TestProxy_NonStreamingUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"claude-sonnet-5","usage":{"input_tokens":40,"output_tokens":10}}`)
	}))
	defer upstream.Close()

	s, sig := newServerTo(t, upstream)
	front := httptest.NewServer(s.Handler())
	defer front.Close()

	resp, err := http.Post(front.URL+"/v1/messages", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body)

	waitReport(t, sig)
	snap := s.stats.snapshot()
	if snap.Totals.InputTokens != 40 || snap.Totals.OutputTokens != 10 {
		t.Errorf("usage mismatch: %+v", snap.Totals)
	}
}

// TestSSEUsageParser_ChunkSplit proves the parser reassembles usage even when
// SSE lines are split across arbitrary read boundaries (the real streaming
// case).
func TestSSEUsageParser_ChunkSplit(t *testing.T) {
	p := newSSEUsageParser()
	for i := 0; i < len(sseBody); i += 7 { // deliberately awkward chunk size
		end := i + 7
		if end > len(sseBody) {
			end = len(sseBody)
		}
		p.feed([]byte(sseBody[i:end]))
	}
	u := p.usage()
	if u.InputTokens != 1200 || u.OutputTokens != 57 || u.CacheReadInputTokens != 800 {
		t.Errorf("chunk-split usage mismatch: %+v", u)
	}
	if u.Model != "claude-opus-4-8" {
		t.Errorf("model mismatch: %q", u.Model)
	}
}

func TestHealthz(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer upstream.Close()
	s, _ := newServerTo(t, upstream)
	front := httptest.NewServer(s.Handler())
	defer front.Close()

	resp, err := http.Get(front.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz status = %d", resp.StatusCode)
	}
}

func TestSingleJoiningSlash(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{"", "/v1/messages", "/v1/messages"},
		{"/anthropic", "/v1/messages", "/anthropic/v1/messages"},
		{"/anthropic/", "/v1/messages", "/anthropic/v1/messages"},
	}
	for _, c := range cases {
		if got := singleJoiningSlash(c.a, c.b); got != c.want {
			t.Errorf("singleJoiningSlash(%q,%q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}
