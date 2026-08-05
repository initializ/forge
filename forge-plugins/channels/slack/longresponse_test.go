package slack

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/initializ/forge/forge-core/a2a"
	"github.com/initializ/forge/forge-core/channels"
)

func TestStripCompressionMarkers(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"no marker", "plain text", "plain text"},
		{
			"trailing marker",
			"Here are the issues.\n\n<<ctxzip:2cda42236849 55_lines_offloaded>>",
			"Here are the issues.",
		},
		{
			"inline marker",
			"before <<ctxzip:abc123abc123 12_rows_offloaded>> after",
			"before  after",
		},
		{
			"multiple markers",
			"<<ctxzip:aaaaaaaaaaaa 1_rows_offloaded>>x<<ctxzip:bbbbbbbbbbbb 2_rows_offloaded>>",
			"x",
		},
		{
			"bare marker no note",
			"data<<ctxzip:0123456789ab>>",
			"data",
		},
		// A "<<ctxzip:" that is not a well-formed marker is left alone rather
		// than eating the rest of the string.
		{"malformed left intact", "text <<ctxzip:not-hex blah", "text <<ctxzip:not-hex blah"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripCompressionMarkers(tt.in); got != tt.want {
				t.Errorf("stripCompressionMarkers(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestExtractText_StripsCompressionMarker pins that a leaked ctxzip marker
// never reaches a Slack channel through the text path.
func TestExtractText_StripsCompressionMarker(t *testing.T) {
	msg := &a2a.Message{Parts: []a2a.Part{
		a2a.NewTextPart("Summary of results. <<ctxzip:2cda42236849 55_lines_offloaded>>"),
	}}
	if got := extractText(msg); strings.Contains(got, "ctxzip") {
		t.Errorf("extractText leaked a compression marker: %q", got)
	}
}

// recordingServer captures every chat.postMessage payload the plugin sends.
func recordingServer(t *testing.T) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var calls []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/chat.postMessage") {
			body, _ := io.ReadAll(r.Body)
			var payload map[string]any
			_ = json.Unmarshal(body, &payload)
			mu.Lock()
			calls = append(calls, payload)
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	return srv, &calls
}

// TestSendChunked_AllChunksThreaded is the regression for the split bug: every
// chunk of a multi-message fallback must carry thread_ts, so none leak to the
// main channel.
func TestSendChunked_AllChunksThreaded(t *testing.T) {
	srv, calls := recordingServer(t)
	defer srv.Close()

	p := New()
	p.botToken = "xoxb-test-token"
	p.apiBase = srv.URL

	event := &channels.ChannelEvent{
		WorkspaceID: "C0123456",
		ThreadID:    "1699.0001",
	}

	// Force multiple chunks: comfortably over the 4000-char split size.
	big := strings.Repeat("line of report text\n", 800)
	if err := p.sendChunked(event, big); err != nil {
		t.Fatalf("sendChunked error: %v", err)
	}

	if len(*calls) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(*calls))
	}
	for i, c := range *calls {
		if c["thread_ts"] != "1699.0001" {
			t.Errorf("chunk %d thread_ts = %v, want 1699.0001 (chunk leaked to main channel)", i, c["thread_ts"])
		}
	}
}

// TestSendChunked_ThreadsUnderMessageID: when the event has no existing thread,
// chunks reply under the originating message id so the whole reply stays in one
// thread rather than splitting across the channel.
func TestSendChunked_ThreadsUnderMessageID(t *testing.T) {
	srv, calls := recordingServer(t)
	defer srv.Close()

	p := New()
	p.botToken = "xoxb-test-token"
	p.apiBase = srv.URL

	event := &channels.ChannelEvent{
		WorkspaceID: "C0123456",
		MessageID:   "1699.9999",
	}

	big := strings.Repeat("line of report text\n", 800)
	if err := p.sendChunked(event, big); err != nil {
		t.Fatalf("sendChunked error: %v", err)
	}

	if len(*calls) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(*calls))
	}
	for i, c := range *calls {
		if c["thread_ts"] != "1699.9999" {
			t.Errorf("chunk %d thread_ts = %v, want 1699.9999", i, c["thread_ts"])
		}
	}
}
