package optimizer

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeDistiller records the transcripts it is asked to distill and returns a
// canned episode, so we can test boundary detection + storage without a network.
type fakeDistiller struct {
	mu        sync.Mutex
	calls     []DistillInput
	procCalls []ProcedureInput
	result    *Episode
}

func (f *fakeDistiller) Distill(_ context.Context, in DistillInput) (*Episode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, in)
	e := *f.result
	return &e, nil
}

func (f *fakeDistiller) DistillProcedure(_ context.Context, in ProcedureInput) (*Episode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.procCalls = append(f.procCalls, in)
	return &Episode{
		Kind:          KindProcedural,
		TaskSignature: "generalized procedure",
		Summary:       "Consolidated from a cluster.",
		Actions:       []string{"step 1", "step 2"},
		Lesson:        "the recurring lesson",
	}, nil
}

func (f *fakeDistiller) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// flakyDistiller fails its first failFirst calls, then succeeds — for testing
// transient-failure retry (MEDIUM #6).
type flakyDistiller struct {
	mu        sync.Mutex
	calls     int
	failFirst int
	result    *Episode
}

func (d *flakyDistiller) Distill(_ context.Context, _ DistillInput) (*Episode, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	if d.calls <= d.failFirst {
		return nil, fmt.Errorf("transient distill failure %d", d.calls)
	}
	e := *d.result
	return &e, nil
}

func (d *flakyDistiller) DistillProcedure(_ context.Context, _ ProcedureInput) (*Episode, error) {
	return &Episode{Kind: KindProcedural}, nil
}

func (d *flakyDistiller) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// TestObserve_RetriesTransientDistillFailure guards MEDIUM #6: a span whose first
// distillation fails must be retried on a later turn, not silently lost. The old
// code advanced the distilled[session] high-water mark before dispatch and only
// deleted seenSpans on failure, so Observe's early-return kept the span from ever
// being revisited.
func TestObserve_RetriesTransientDistillFailure(t *testing.T) {
	store, err := NewFileMemoryStore(filepath.Join(t.TempDir(), "mem.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	fd := &flakyDistiller{failFirst: 1, result: &Episode{TaskSignature: "t", Summary: "s", Outcome: OutcomeSuccess}}
	former := newFormer(t, MemoryFormerConfig{Store: store, Distiller: fd, Repo: "forge", Commit: "c"})

	// A body with one COMPLETED span (two user instructions, activity between).
	body := []byte(`{
		"model": "claude-opus-4-8",
		"messages": [
			{"role": "user", "content": "do the task"},
			{"role": "assistant", "content": [{"type": "tool_use", "name": "Edit", "input": {"file": "a.go"}}]},
			{"role": "user", "content": [{"type": "tool_result", "content": "ok"}]},
			{"role": "assistant", "content": [{"type": "text", "text": "Done."}]},
			{"role": "user", "content": "now the next thing"}
		]
	}`)

	// Turn 1: distillation fails → no episode, but the span must remain retryable.
	former.Observe("s1", body, http.Header{}, "https://api.anthropic.com")
	former.waitAsync()
	if eps, _ := store.ListEpisodes("", 0); len(eps) != 0 {
		t.Fatalf("expected no episode after the failed attempt, got %d", len(eps))
	}

	// Turn 2: the same completed span is re-observed and retried → now succeeds.
	former.Observe("s1", body, http.Header{}, "https://api.anthropic.com")
	former.waitAsync()
	eps := waitForEpisodes(t, store, 1)
	if len(eps) != 1 {
		t.Fatalf("expected 1 episode after retry, got %d", len(eps))
	}
	if fd.callCount() < 2 {
		t.Errorf("expected the span to be retried (>=2 distill calls), got %d", fd.callCount())
	}
}

// waitForEpisodes polls the store until it holds at least n episodes or times out.
func waitForEpisodes(t *testing.T, store MemoryStore, n int) []Episode {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		eps, err := store.ListEpisodes("", 0)
		if err != nil {
			t.Fatalf("ListEpisodes: %v", err)
		}
		if len(eps) >= n {
			return eps
		}
		time.Sleep(10 * time.Millisecond)
	}
	eps, _ := store.ListEpisodes("", 0)
	t.Fatalf("timed out waiting for %d episodes, have %d", n, len(eps))
	return eps
}

func TestMemoryFormer_FormsEpisodeOnTaskBoundary(t *testing.T) {
	store, err := NewFileMemoryStore(filepath.Join(t.TempDir(), "mem.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	fd := &fakeDistiller{result: &Episode{
		TaskSignature: "add retry to http client",
		Summary:       "Added exponential backoff retry.",
		Outcome:       OutcomeSuccess,
		Files:         []string{"client.go"},
	}}
	former := newFormer(t, MemoryFormerConfig{
		Store:     store,
		Distiller: fd,
		Repo:      "forge",
		Commit:    "abc123",
	})

	// Turn 1: a single in-progress task (one user instruction, plus tool
	// activity). No completed span yet → no distillation.
	body1 := []byte(`{
		"model": "claude-opus-4-8",
		"messages": [
			{"role": "user", "content": "add retry logic to the http client"},
			{"role": "assistant", "content": [{"type": "tool_use", "name": "Edit", "input": {"file": "client.go"}}]},
			{"role": "user", "content": [{"type": "tool_result", "content": "ok"}]}
		]
	}`)
	former.Observe("sess1", body1, http.Header{}, "https://api.anthropic.com")
	if got := fd.callCount(); got != 0 {
		t.Fatalf("expected no distillation for in-progress task, got %d calls", got)
	}

	// Turn 2: a SECOND user instruction appears → the first task span is now
	// complete and should be distilled exactly once.
	body2 := []byte(`{
		"model": "claude-opus-4-8",
		"messages": [
			{"role": "user", "content": "add retry logic to the http client"},
			{"role": "assistant", "content": [{"type": "tool_use", "name": "Edit", "input": {"file": "client.go"}}]},
			{"role": "user", "content": [{"type": "tool_result", "content": "ok"}]},
			{"role": "assistant", "content": [{"type": "text", "text": "Done."}]},
			{"role": "user", "content": "now add tests"}
		]
	}`)
	former.Observe("sess1", body2, http.Header{}, "https://api.anthropic.com")

	eps := waitForEpisodes(t, store, 1)
	if len(eps) != 1 {
		t.Fatalf("expected exactly 1 episode, got %d", len(eps))
	}
	e := eps[0]
	if e.TaskSignature != "add retry to http client" {
		t.Errorf("task signature = %q", e.TaskSignature)
	}
	if e.Repo != "forge" || e.CodeState.Commit != "abc123" {
		t.Errorf("grounding not stamped: repo=%q commit=%q", e.Repo, e.CodeState.Commit)
	}
	if e.SourceKind != "agent_inference" || e.Binding != "inferred" {
		t.Errorf("provenance not stamped: source=%q binding=%q", e.SourceKind, e.Binding)
	}
	if e.SessionID != "sess1" {
		t.Errorf("session id = %q", e.SessionID)
	}

	// Re-observing the same body must NOT re-distill (span already done).
	former.Observe("sess1", body2, http.Header{}, "https://api.anthropic.com")
	time.Sleep(50 * time.Millisecond)
	if got := fd.callCount(); got != 1 {
		t.Fatalf("expected exactly 1 distillation total, got %d", got)
	}
}

func TestMemoryFormer_SkipsChitchatSpans(t *testing.T) {
	store, err := NewFileMemoryStore(filepath.Join(t.TempDir(), "mem.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	fd := &fakeDistiller{result: &Episode{TaskSignature: "x", Summary: "y", Outcome: OutcomeSuccess}}
	former := newFormer(t, MemoryFormerConfig{Store: store, Distiller: fd, Repo: "forge"})

	// Two instructions but the completed span has NO tool activity → skip.
	body := []byte(`{
		"model": "claude-opus-4-8",
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "assistant", "content": [{"type": "text", "text": "hi"}]},
			{"role": "user", "content": "what can you do"}
		]
	}`)
	former.Observe("sess2", body, http.Header{}, "https://api.anthropic.com")
	time.Sleep(50 * time.Millisecond)
	if got := fd.callCount(); got != 0 {
		t.Fatalf("expected chit-chat span to be skipped, got %d distillations", got)
	}
}

func TestParseEpisodeJSON_ToleratesFence(t *testing.T) {
	e, err := parseEpisodeJSON("```json\n{\"task_signature\":\"fix bug\",\"summary\":\"s\",\"outcome\":\"success\"}\n```")
	if err != nil {
		t.Fatal(err)
	}
	if e.TaskSignature != "fix bug" || e.Outcome != OutcomeSuccess {
		t.Fatalf("bad parse: %+v", e)
	}
}

func TestRecordRecall_CountsDistinctSessions(t *testing.T) {
	store, err := NewFileMemoryStore(filepath.Join(t.TempDir(), "mem.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// ep_a recalled into 2 distinct sessions (one twice → still counts once);
	// ep_b recalled into 1 session.
	_ = store.RecordRecall("s1", []string{"ep_a", "ep_b"})
	_ = store.RecordRecall("s2", []string{"ep_a"})
	_ = store.RecordRecall("s2", []string{"ep_a"}) // duplicate session for ep_a

	counts, err := store.RecallCounts()
	if err != nil {
		t.Fatal(err)
	}
	if counts["ep_a"].Count != 2 {
		t.Errorf("ep_a distinct-session count = %d, want 2", counts["ep_a"].Count)
	}
	if counts["ep_b"].Count != 1 {
		t.Errorf("ep_b count = %d, want 1", counts["ep_b"].Count)
	}
	if counts["ep_a"].LastRecalled == "" {
		t.Errorf("ep_a last_recalled not set")
	}
}
