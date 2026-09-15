package optimizer

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/initializ/ctxzip/ccr"
)

// TestInbandExpand_EndToEnd drives the full zero-config retrieval loop:
//  1. compress a bulky tool result (offloads to store, leaves a marker),
//  2. send the request through the proxy with in-band expansion on,
//  3. a mock upstream first calls context_expand (round 1), then — once the
//     proxy resolves it and re-issues — returns a final text answer (round 2),
//  4. assert the client receives the final answer and never sees the tool.
func TestInbandExpand_EndToEnd(t *testing.T) {
	c := newTestCompressor(t)

	// Prime the store: compress a bulky tool result and grab its marker hash.
	big := bigJSONText()
	primeReq := map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "find failures"},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "tu_1", "content": big},
		}},
		map[string]any{"role": "user", "content": "?"},
	}}
	primeBody, _ := json.Marshal(primeReq)
	primed, st, _ := c.Transform(primeBody)
	if st.Markers < 1 {
		t.Fatalf("expected a marker after transform, got %+v", st)
	}
	hash := ccr.ExtractHashes(toolResultContent(t, messagesOf(t, primed)[1]))[0]

	// Mock upstream: round 1 → context_expand tool_use; round 2 → final text.
	var round int32
	var sawExpandResult atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// The context_expand tool must be present in the injected request.
		if !strings.Contains(string(body), "context_expand") {
			t.Errorf("context_expand tool not injected into upstream request")
		}
		n := atomic.AddInt32(&round, 1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			// Ask to expand the marker.
			resp := map[string]any{
				"id": "msg_1", "type": "message", "role": "assistant",
				"model": "claude-opus-4-8", "stop_reason": "tool_use",
				"content": []any{
					map[string]any{"type": "tool_use", "id": "toolu_x", "name": "context_expand", "input": map[string]any{"hash": hash}},
				},
				"usage": map[string]any{"input_tokens": 100, "output_tokens": 5},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		// Round 2: the re-issued request must carry the resolved original.
		if strings.Contains(string(body), "row 200 processed") {
			sawExpandResult.Store(true)
		}
		resp := map[string]any{
			"id": "msg_2", "type": "message", "role": "assistant",
			"model": "claude-opus-4-8", "stop_reason": "end_turn",
			"content": []any{
				map[string]any{"type": "text", "text": "Row 200 processed successfully; no failures."},
			},
			"usage": map[string]any{"input_tokens": 200, "output_tokens": 12},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer upstream.Close()

	s, sig := newServerTo(t, upstream)
	s.compressor = c
	s.inbandExpand = true
	front := httptest.NewServer(s.Handler())
	defer front.Close()

	// Client asks for streaming — the proxy must buffer, resolve, and
	// re-synthesize SSE.
	reqBody := `{"model":"claude-opus-4-8","stream":true,"messages":[` +
		`{"role":"user","content":"find failures"},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":` + jsonString(big) + `}]},` +
		`{"role":"user","content":"what failed?"}]}`
	resp, err := http.Post(front.URL+"/v1/messages", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)

	waitReport(t, sig)

	if atomic.LoadInt32(&round) != 2 {
		t.Fatalf("expected 2 upstream rounds (expand + final), got %d", round)
	}
	if !sawExpandResult.Load() {
		t.Errorf("re-issued request did not carry the resolved original")
	}
	// Client got the FINAL answer, re-synthesized as SSE, and never the tool.
	sout := string(out)
	if !strings.Contains(sout, "no failures") {
		t.Errorf("client did not receive final answer; got:\n%s", sout)
	}
	if !strings.Contains(sout, "event: message_start") || !strings.Contains(sout, "event: message_stop") {
		t.Errorf("response was not re-synthesized as SSE:\n%s", sout)
	}
	if strings.Contains(sout, "context_expand") {
		t.Errorf("client saw the context_expand tool_use (should have been intercepted):\n%s", sout)
	}
	if s.stats.snapshot().Totals.Expansions != 1 {
		t.Errorf("expansion not recorded: %+v", s.stats.snapshot().Totals)
	}
}

func TestInjectExpandTool(t *testing.T) {
	// No tools yet → creates the array with context_expand.
	out := injectExpandTool([]byte(`{"model":"x","messages":[]}`))
	if !strings.Contains(string(out), `"context_expand"`) || !strings.Contains(string(out), `"input_schema"`) {
		t.Errorf("tool not injected with input_schema: %s", out)
	}
	// Idempotent.
	if out2 := injectExpandTool(out); strings.Count(string(out2), "context_expand") != strings.Count(string(out), "context_expand") {
		t.Errorf("injection not idempotent")
	}
	// Existing tools are preserved, ours appended.
	out3 := injectExpandTool([]byte(`{"tools":[{"name":"read_file","description":"d","input_schema":{}}],"messages":[]}`))
	if !strings.Contains(string(out3), "read_file") || !strings.Contains(string(out3), "context_expand") {
		t.Errorf("existing tool not preserved alongside injection: %s", out3)
	}
}

func jsonString(s string) string { b, _ := json.Marshal(s); return string(b) }
