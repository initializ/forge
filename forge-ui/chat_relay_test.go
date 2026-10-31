package forgeui

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// A result frame carrying a model-generated image inline (base64 on one
// `data:` line) is ~1 MB+, far past bufio.Scanner's default 64 KiB line cap.
// relayAgentSSE must relay it intact rather than dropping it (the "chat never
// shows anything" bug).
func TestRelayAgentSSE_LargeMediaFrame(t *testing.T) {
	bigData := `{"status":{"message":{"parts":[{"kind":"file","file":{"mimeType":"image/jpeg","bytes":"` +
		strings.Repeat("A", 2<<20) + `"}}]}}}` // ~2 MiB of base64 on one line
	agentStream := "event: result\ndata: " + bigData + "\n\n"

	rec := httptest.NewRecorder()
	relayAgentSSE(context.Background(), rec, rec, strings.NewReader(agentStream))

	out := rec.Body.String()
	if strings.Contains(out, "event: error") {
		t.Fatalf("large frame should relay cleanly, got error event: %.200s", out)
	}
	if !strings.Contains(out, "event: result") || !strings.Contains(out, bigData) {
		t.Fatalf("large result frame was not relayed intact (len=%d)", len(out))
	}
}

// A frame larger than maxSSEFrameBytes must surface an `error` event, not end
// silently with no reply.
func TestRelayAgentSSE_OversizeFrameSurfacesError(t *testing.T) {
	// One data line past the cap.
	oversize := "event: result\ndata: " + strings.Repeat("A", maxSSEFrameBytes+1024) + "\n\n"

	rec := httptest.NewRecorder()
	relayAgentSSE(context.Background(), rec, rec, strings.NewReader(oversize))

	if !strings.Contains(rec.Body.String(), "event: error") {
		t.Fatalf("a frame over the cap must emit an error event, got: %.200s", rec.Body.String())
	}
}

// Normal small frames relay unchanged, frame-by-frame.
func TestRelayAgentSSE_SmallFrames(t *testing.T) {
	var b strings.Builder
	for i := range 3 {
		fmt.Fprintf(&b, "event: status\ndata: {\"n\":%d}\n\n", i)
	}
	rec := httptest.NewRecorder()
	relayAgentSSE(context.Background(), rec, rec, strings.NewReader(b.String()))

	out := rec.Body.String()
	for i := range 3 {
		if !strings.Contains(out, fmt.Sprintf(`{"n":%d}`, i)) {
			t.Errorf("frame %d not relayed: %s", i, out)
		}
	}
	if strings.Contains(out, "event: error") {
		t.Errorf("clean small frames must not emit an error event")
	}
}
