package optimizer

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
)

func newRecallFormer(t *testing.T, repo string, seed []Episode) *MemoryFormer {
	t.Helper()
	store, err := NewFileMemoryStore(filepath.Join(t.TempDir(), "mem.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range seed {
		if err := store.WriteEpisode(e); err != nil {
			t.Fatal(err)
		}
	}
	return NewMemoryFormer(MemoryFormerConfig{
		Store:  store,
		Repo:   repo,
		Recall: RecallConfig{Enabled: true},
	})
}

// systemBlocks decodes the system field of a request body into text strings.
func systemBlocks(t *testing.T, body []byte) []string {
	t.Helper()
	var top struct {
		System json.RawMessage `json:"system"`
	}
	if err := json.Unmarshal(body, &top); err != nil {
		t.Fatal(err)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(top.System, &blocks); err != nil {
		t.Fatalf("system not a block array: %v", err)
	}
	out := make([]string, len(blocks))
	for i, b := range blocks {
		out[i] = b.Text
	}
	return out
}

func TestRecall_InjectsRelevantEpisodes(t *testing.T) {
	former := newRecallFormer(t, "forge", []Episode{
		{ID: "1", Repo: "forge", SessionID: "old", TaskSignature: "add retry to http client",
			Lesson: "reuse the shared retry helper", Outcome: OutcomeSuccess, Files: []string{"client.go"}},
		{ID: "2", Repo: "other", SessionID: "old", TaskSignature: "unrelated repo", Outcome: OutcomeSuccess},
	})

	body := []byte(`{"model":"claude-opus-4-8","system":[{"type":"text","text":"You are Claude Code."}],"messages":[{"role":"user","content":"add a retry"}]}`)
	out, n := former.Inject("sessNew", body)
	if n != 1 {
		t.Fatalf("expected 1 episode injected, got %d", n)
	}
	blocks := systemBlocks(t, out)
	if len(blocks) != 2 {
		t.Fatalf("expected original + recall block, got %d blocks", len(blocks))
	}
	if blocks[0] != "You are Claude Code." {
		t.Errorf("original system block disturbed: %q", blocks[0])
	}
	if !bytes.Contains([]byte(blocks[1]), []byte("reuse the shared retry helper")) {
		t.Errorf("recall block missing lesson: %q", blocks[1])
	}
	if bytes.Contains(out, []byte("unrelated repo")) {
		t.Errorf("recall leaked an episode from another repo")
	}
}

func TestRecall_FrozenPerSession_ByteStable(t *testing.T) {
	former := newRecallFormer(t, "forge", []Episode{
		{ID: "1", Repo: "forge", SessionID: "old", TaskSignature: "task a", Lesson: "l", Outcome: OutcomeSuccess, CreatedAt: "2026-01-01T00:00:00Z"},
	})
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)

	out1, _ := former.Inject("s1", body)

	// A new episode is written mid-session; the frozen block must NOT change.
	_ = former.store.WriteEpisode(Episode{ID: "2", Repo: "forge", SessionID: "old", TaskSignature: "task b", Lesson: "l2", Outcome: OutcomeSuccess, CreatedAt: "2026-02-01T00:00:00Z"})

	out2, _ := former.Inject("s1", body)
	if !bytes.Equal(out1, out2) {
		t.Fatalf("recall injection not byte-stable across a session:\n1: %s\n2: %s", out1, out2)
	}
}

func TestRecall_ExcludesOwnSession(t *testing.T) {
	former := newRecallFormer(t, "forge", []Episode{
		{ID: "1", Repo: "forge", SessionID: "sMe", TaskSignature: "my own task", Lesson: "l", Outcome: OutcomeSuccess},
	})
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	out, n := former.Inject("sMe", body)
	if n != 0 {
		t.Fatalf("expected own-session episode to be excluded, got %d", n)
	}
	if !bytes.Equal(out, body) {
		t.Errorf("body should be unchanged when nothing to recall")
	}
}

func TestRecall_WrapsStringSystem(t *testing.T) {
	former := newRecallFormer(t, "forge", []Episode{
		{ID: "1", Repo: "forge", SessionID: "old", TaskSignature: "t", Lesson: "l", Outcome: OutcomeSuccess},
	})
	body := []byte(`{"model":"m","system":"plain string prompt","messages":[{"role":"user","content":"hi"}]}`)
	out, n := former.Inject("s1", body)
	if n != 1 {
		t.Fatalf("expected 1 injected, got %d", n)
	}
	blocks := systemBlocks(t, out)
	if len(blocks) != 2 || blocks[0] != "plain string prompt" {
		t.Fatalf("string system not wrapped correctly: %#v", blocks)
	}
}

func TestRecall_DisabledIsNoop(t *testing.T) {
	store, _ := NewFileMemoryStore(filepath.Join(t.TempDir(), "m.jsonl"))
	_ = store.WriteEpisode(Episode{ID: "1", Repo: "forge", SessionID: "old", TaskSignature: "t", Lesson: "l", Outcome: OutcomeSuccess})
	former := NewMemoryFormer(MemoryFormerConfig{Store: store, Repo: "forge"}) // Recall disabled
	body := []byte(`{"model":"m","messages":[]}`)
	out, n := former.Inject("s1", body)
	if n != 0 || !bytes.Equal(out, body) {
		t.Fatalf("recall should be a no-op when disabled")
	}
}

// lastUserText returns the concatenated text of the last message's content.
func lastUserText(t *testing.T, body []byte) string {
	t.Helper()
	var top struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &top); err != nil || len(top.Messages) == 0 {
		t.Fatalf("no messages: %v", err)
	}
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	_ = json.Unmarshal(top.Messages[len(top.Messages)-1], &m)
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return s
	}
	var blocks []struct {
		Type, Text string
	}
	_ = json.Unmarshal(m.Content, &blocks)
	var b []byte
	for _, blk := range blocks {
		b = append(b, blk.Text...)
		b = append(b, ' ')
	}
	return string(b)
}

func TestTailOverlay_InjectsTaskRelevantEpisodeIntoTail(t *testing.T) {
	store, err := NewFileMemoryStore(filepath.Join(t.TempDir(), "mem.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// A is higher-confidence but unrelated → it wins the single frozen slot.
	// B is task-relevant but lower-confidence → it spills to the tail overlay.
	_ = store.WriteEpisode(Episode{ID: "A", Repo: "forge", SessionID: "old", TaskSignature: "refactor the build system",
		Lesson: "use the make targets", Entities: []string{"Makefile"}, Outcome: OutcomeSuccess, Confidence: 0.9})
	_ = store.WriteEpisode(Episode{ID: "B", Repo: "forge", SessionID: "old", TaskSignature: "add retry to client",
		Lesson: "reuse the shared retry helper", Entities: []string{"forge-core/llm/client.go"}, Outcome: OutcomeSuccess, Confidence: 0.6})

	former := NewMemoryFormer(MemoryFormerConfig{
		Store: store, Distiller: &fakeDistiller{result: &Episode{}}, Repo: "forge",
		Recall: RecallConfig{Enabled: true, TopN: 1}, // frozen holds only the top (unrelated) one
	})
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"add retry to forge-core/llm/client.go please"}]}`)
	out, n := former.Inject("sX", body)
	if n == 0 {
		t.Fatalf("expected memory injected")
	}
	// The task-relevant episode B rides in the TAIL (last user message).
	tail := lastUserText(t, out)
	if !bytes.Contains([]byte(tail), []byte("add retry to client")) {
		t.Errorf("task-relevant memory not in tail overlay: %q", tail)
	}
	// The user's original instruction is still there.
	if !bytes.Contains([]byte(tail), []byte("client.go please")) {
		t.Errorf("original user instruction lost: %q", tail)
	}
	// The unrelated memory (A) must NOT be in the tail.
	if bytes.Contains([]byte(tail), []byte("refactor the build system")) {
		t.Errorf("unrelated memory leaked into tail")
	}
	// The frozen system block holds the unrelated high-confidence memory (A).
	sys := systemBlocks(t, out)
	if !bytes.Contains([]byte(sys[len(sys)-1]), []byte("refactor the build system")) {
		t.Errorf("frozen block should hold the top task-agnostic memory (A): %q", sys[len(sys)-1])
	}
}

func TestTailOverlay_SkipsToolResultTurns(t *testing.T) {
	store, _ := NewFileMemoryStore(filepath.Join(t.TempDir(), "mem.jsonl"))
	_ = store.WriteEpisode(Episode{ID: "B", Repo: "forge", SessionID: "old", TaskSignature: "add retry to client",
		Lesson: "reuse the shared retry helper", Entities: []string{"forge-core/llm/client.go"}, Outcome: OutcomeSuccess, Confidence: 0.6})
	former := NewMemoryFormer(MemoryFormerConfig{Store: store, Repo: "forge", Recall: RecallConfig{Enabled: true}})

	// Latest message is a tool_result continuation, not a fresh instruction.
	body := []byte(`{"model":"m","messages":[
		{"role":"user","content":"add retry to forge-core/llm/client.go"},
		{"role":"assistant","content":[{"type":"tool_use","name":"Edit","input":{"file":"client.go"}}]},
		{"role":"user","content":[{"type":"tool_result","content":"done"}]}
	]}`)
	blk, n := former.tailOverlay("sX", body, "forge", nil)
	if blk != "" || n != 0 {
		t.Fatalf("tail overlay should not fire on a tool_result turn, got n=%d", n)
	}
}
