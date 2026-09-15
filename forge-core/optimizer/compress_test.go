package optimizer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/initializ/ctxzip/ccr"
)

func newTestCompressor(t *testing.T) *Compressor {
	t.Helper()
	c, err := NewCompressor(CompressConfig{
		StorePath: filepath.Join(t.TempDir(), "ctxzip.db"),
		MinTokens: 10,
	})
	if err != nil {
		t.Fatalf("NewCompressor: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// bigJSONText returns a large, highly compressible JSON-array string — the kind
// of bulky tool output ctxzip offloads.
func bigJSONText() string {
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < 400; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":%d,"status":"ok","message":"row %d processed successfully with no anomalies detected"}`, i, i)
	}
	b.WriteString("]")
	return b.String()
}

// messagesOf extracts the messages array from a request body as raw elements.
func messagesOf(t *testing.T, body []byte) []json.RawMessage {
	t.Helper()
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	var msgs []json.RawMessage
	if err := json.Unmarshal(root["messages"], &msgs); err != nil {
		t.Fatalf("unmarshal messages: %v", err)
	}
	return msgs
}

func compact(t *testing.T, raw []byte) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		t.Fatalf("compact: %v", err)
	}
	return buf.String()
}

// TestTransform_CompressesHistory_PreservesAnchor builds a request whose first
// message is the anchor (never compressed) and whose middle message holds a
// bulky tool_result. It asserts: the anchor is byte-identical in the output,
// the bulky tool_result is compressed to a marker, and small blocks are left
// alone.
func TestTransform_CompressesHistory_PreservesAnchor(t *testing.T) {
	c := newTestCompressor(t)
	big := bigJSONText()

	req := map[string]any{
		"model":      "claude-opus-4-8",
		"max_tokens": 1024,
		"messages": []any{
			// index 0: frozen (has cache_control)
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "system-ish framing", "cache_control": map[string]any{"type": "ephemeral"}},
			}},
			// index 1: assistant tool_use
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": "tu_1", "name": "read_file", "input": map[string]any{"path": "/x"}},
			}},
			// index 2: LIVE zone — bulky tool_result
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "tu_1", "content": big},
			}},
			// index 3: current turn (protected)
			map[string]any{"role": "user", "content": "what did that return?"},
		},
	}
	body, _ := json.Marshal(req)

	out, st, err := c.Transform(body)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if st.SavedTokens <= 0 || st.Blocks != 1 || st.Markers < 1 {
		t.Fatalf("expected one compressed block with savings, got %+v", st)
	}

	inMsgs := messagesOf(t, body)
	outMsgs := messagesOf(t, out)

	// Anchor prefix (index 0) must be byte-identical.
	if compact(t, inMsgs[0]) != compact(t, outMsgs[0]) {
		t.Errorf("anchor message 0 was modified:\n in=%s\nout=%s", inMsgs[0], outMsgs[0])
	}
	// Live tool_result (index 2) must now carry a ctxzip marker. Note json
	// escapes "<<" as << on the wire; the "ctxzip:" hash token is
	// unescaped, and the model decodes the JSON before reading it either way.
	if !bytes.Contains(outMsgs[2], []byte("ctxzip:")) {
		t.Errorf("live tool_result not compressed: %s", outMsgs[2])
	}
	// It must be smaller than the original (bulk offloaded).
	if len(outMsgs[2]) >= len(inMsgs[2]) {
		t.Errorf("compressed block not smaller: in=%d out=%d", len(inMsgs[2]), len(outMsgs[2]))
	}
	// Current turn (index 3) untouched.
	if compact(t, inMsgs[3]) != compact(t, outMsgs[3]) {
		t.Errorf("current turn was modified")
	}
	// System directive injected.
	if !bytes.Contains(out, []byte("Compressed context")) {
		t.Errorf("system directive not injected")
	}

	// Reversibility: the offloaded original must be retrievable from the store
	// by the marker's hash — this is what phase-3 context_expand will do.
	content := toolResultContent(t, outMsgs[2])
	hashes := ccr.ExtractHashes(content)
	if len(hashes) == 0 {
		t.Fatalf("no marker hash in compressed content: %s", content)
	}
	entry, ok := c.Store().Get(hashes[0])
	if !ok {
		t.Fatalf("marker hash %s not retrievable from store", hashes[0])
	}
	if !bytes.Contains(entry.Original, []byte("row 200 processed")) {
		t.Errorf("retrieved original missing offloaded row 200")
	}
}

// toolResultContent digs the tool_result content string out of a message.
func toolResultContent(t *testing.T, msgRaw json.RawMessage) string {
	t.Helper()
	var msg struct {
		Content []struct {
			Type    string          `json:"type"`
			Content json.RawMessage `json:"content"`
		} `json:"content"`
	}
	if err := json.Unmarshal(msgRaw, &msg); err != nil {
		t.Fatalf("unmarshal message: %v", err)
	}
	for _, b := range msg.Content {
		if b.Type == "tool_result" {
			var s string
			if json.Unmarshal(b.Content, &s) == nil {
				return s
			}
		}
	}
	return ""
}

// TestTransform_CompressesBlocksBearingCacheControl is the regression test for
// the real-Claude-Code failure: under aggressive prompt caching, bulky blocks
// carry cache_control and sit "before the last breakpoint". They must still be
// compressed — deterministic compression is cache-safe, and gating on
// breakpoints compressed nothing.
func TestTransform_CompressesBlocksBearingCacheControl(t *testing.T) {
	c := newTestCompressor(t)
	big := bigJSONText()

	req := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "anchor task"},
			// A bulky tool_result that ALSO carries a cache_control breakpoint,
			// as Claude Code emits on cached history. Must still compress.
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "tu_1", "content": big, "cache_control": map[string]any{"type": "ephemeral"}},
			}},
			map[string]any{"role": "assistant", "content": "working on it"},
		},
	}
	body, _ := json.Marshal(req)

	_, st, err := c.Transform(body)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if st.Blocks != 1 || st.SavedTokens <= 0 {
		t.Fatalf("cache_control-bearing block was not compressed: %+v", st)
	}
}

// TestTransform_SkipsContextExpandResults proves a tool_result whose tool_use is
// context_expand is never recompressed (the tail-chase guard).
func TestTransform_SkipsContextExpandResults(t *testing.T) {
	c := newTestCompressor(t)
	big := bigJSONText()

	req := map[string]any{
		"model": "claude-opus-4-8",
		"messages": []any{
			// assistant asks to expand
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": "tu_exp", "name": "context_expand", "input": map[string]any{"hash": "abc123"}},
			}},
			// expand result carries the (bulky) original back — MUST be skipped
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "tu_exp", "content": big},
			}},
			map[string]any{"role": "user", "content": "thanks"},
		},
	}
	body, _ := json.Marshal(req)

	out, st, err := c.Transform(body)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if st.Blocks != 0 {
		t.Errorf("context_expand result should not be compressed, got %+v", st)
	}
	if ccr.HasMarker(string(out)) {
		t.Errorf("a marker was created for context_expand content (tail-chase)")
	}
}

// TestTransform_Deterministic asserts the same input compresses to identical
// bytes on repeat — the property that keeps re-sent history byte-stable and
// provider caches warm.
func TestTransform_Deterministic(t *testing.T) {
	c := newTestCompressor(t)
	big := bigJSONText()
	req := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "find the failing rows"},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "tu_1", "content": big},
			}},
			map[string]any{"role": "user", "content": "?"},
		},
	}
	body, _ := json.Marshal(req)

	out1, st1, _ := c.Transform(body)
	out2, st2, _ := c.Transform(body)
	if !bytes.Equal(out1, out2) {
		t.Errorf("non-deterministic output across identical inputs")
	}
	if st1 != st2 {
		t.Errorf("non-deterministic stats: %+v vs %+v", st1, st2)
	}
}

// TestTransform_NoMessagesUnchanged: bodies without messages pass through.
func TestTransform_NoMessagesUnchanged(t *testing.T) {
	c := newTestCompressor(t)
	body := []byte(`{"model":"x","max_tokens":10}`)
	out, st, err := c.Transform(body)
	if err != nil || !bytes.Equal(out, body) || st.Blocks != 0 {
		t.Errorf("expected passthrough, got out=%s st=%+v err=%v", out, st, err)
	}
}

func TestInjectDirective(t *testing.T) {
	// string system
	s := json.RawMessage(`"you are helpful"`)
	out, changed := injectDirective(s)
	if !changed || !bytes.Contains(out, []byte("Compressed context")) {
		t.Errorf("string system: directive not appended: %s", out)
	}
	// idempotent
	if _, changed2 := injectDirective(out); changed2 {
		t.Errorf("directive injection not idempotent for string system")
	}

	// array system with cache_control on the (only) block — directive must be
	// appended AFTER it, leaving that block intact.
	arr := json.RawMessage(`[{"type":"text","text":"base","cache_control":{"type":"ephemeral"}}]`)
	out2, changed3 := injectDirective(arr)
	if !changed3 {
		t.Fatalf("array system: expected change")
	}
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(out2, &blocks); err != nil {
		t.Fatalf("array system output not array: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if _, ok := blocks[0]["cache_control"]; !ok {
		t.Errorf("first (cached) block lost its cache_control")
	}
}
