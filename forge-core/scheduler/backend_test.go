package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeStore is an in-memory ScheduleStore used by Backend tests so
// the assertions stay focused on Backend semantics rather than the
// markdown-file persistence shipping with MemoryScheduleStore.
type fakeStore struct {
	mu        sync.Mutex
	schedules map[string]Schedule
	history   []HistoryEntry
}

func newFakeStore() *fakeStore {
	return &fakeStore{schedules: map[string]Schedule{}}
}

func (f *fakeStore) List(_ context.Context) ([]Schedule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Schedule, 0, len(f.schedules))
	for _, s := range f.schedules {
		out = append(out, s)
	}
	return out, nil
}

func (f *fakeStore) Get(_ context.Context, id string) (*Schedule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.schedules[id]
	if !ok {
		return nil, nil
	}
	return &s, nil
}

func (f *fakeStore) Set(_ context.Context, s Schedule) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.schedules[s.ID] = s
	return nil
}

func (f *fakeStore) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.schedules, id)
	return nil
}

func (f *fakeStore) RecordRun(_ context.Context, e HistoryEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.history = append(f.history, e)
	return nil
}

func (f *fakeStore) History(_ context.Context, _ string, limit int) ([]HistoryEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit > len(f.history) || limit == 0 {
		limit = len(f.history)
	}
	return append([]HistoryEntry(nil), f.history[:limit]...), nil
}

type fakeLogger struct{}

func (fakeLogger) Info(string, map[string]any)  {}
func (fakeLogger) Warn(string, map[string]any)  {}
func (fakeLogger) Error(string, map[string]any) {}

// newTestBackend constructs a FileBackend with the fake store; the
// underlying Scheduler is created but Start is NOT called so the
// 30s ticker stays asleep. The Sync / List / Set / Delete tests run
// against the store via the Backend surface, which is the point.
func newTestBackend(t *testing.T) (*FileBackend, *fakeStore) {
	t.Helper()
	store := newFakeStore()
	sched := New(store, func(_ context.Context, _ Schedule) error { return nil }, fakeLogger{}, nil)
	return NewFileBackend(store, sched), store
}

// TestFileBackend_SyncUpsertsDeclared verifies the basic Sync path:
// yaml-sourced entries get inserted, repeated Sync calls are
// idempotent.
func TestFileBackend_SyncUpsertsDeclared(t *testing.T) {
	b, store := newTestBackend(t)
	ctx := context.Background()

	declared := []Schedule{
		{ID: "daily", Cron: "0 9 * * *", Task: "morning report", Source: SourceYAML, Enabled: true, Created: time.Now()},
		{ID: "hourly", Cron: "@hourly", Task: "heartbeat", Source: SourceYAML, Enabled: true, Created: time.Now()},
	}
	if err := b.Sync(ctx, declared); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := len(store.schedules); got != 2 {
		t.Fatalf("after Sync: %d schedules, want 2", got)
	}

	// Second Sync with same input is a no-op functionally.
	if err := b.Sync(ctx, declared); err != nil {
		t.Fatalf("Sync (idempotent): %v", err)
	}
	if got := len(store.schedules); got != 2 {
		t.Fatalf("after idempotent Sync: %d schedules, want 2", got)
	}
}

// TestFileBackend_SyncPrunesRemovedYAMLEntries verifies the
// reconciliation behavior: a yaml-sourced entry dropped from
// forge.yaml gets removed on Sync.
func TestFileBackend_SyncPrunesRemovedYAMLEntries(t *testing.T) {
	b, store := newTestBackend(t)
	ctx := context.Background()

	first := []Schedule{
		{ID: "a", Cron: "@hourly", Task: "t", Source: SourceYAML, Enabled: true, Created: time.Now()},
		{ID: "b", Cron: "@hourly", Task: "t", Source: SourceYAML, Enabled: true, Created: time.Now()},
	}
	if err := b.Sync(ctx, first); err != nil {
		t.Fatalf("Sync first: %v", err)
	}

	// Remove "b" from the declared set — Sync should delete it.
	second := []Schedule{
		{ID: "a", Cron: "@hourly", Task: "t", Source: SourceYAML, Enabled: true, Created: time.Now()},
	}
	if err := b.Sync(ctx, second); err != nil {
		t.Fatalf("Sync second: %v", err)
	}
	if _, exists := store.schedules["b"]; exists {
		t.Errorf("removed yaml-sourced entry 'b' was not pruned by Sync")
	}
	if _, exists := store.schedules["a"]; !exists {
		t.Errorf("retained entry 'a' was incorrectly pruned")
	}
}

// TestFileBackend_SyncPreservesLLMSourced confirms the bug-fix
// invariant: LLM-set schedules MUST survive a Sync that doesn't
// list them. The cluster (in K8s mode) and the LLM (in either
// mode) own those entries; the declarative reconcile path doesn't.
func TestFileBackend_SyncPreservesLLMSourced(t *testing.T) {
	b, store := newTestBackend(t)
	ctx := context.Background()

	// Seed an LLM-sourced entry directly into the store (skipping
	// Sync — that's the case where the user's chat created a
	// schedule).
	llmSched := Schedule{ID: "from-chat", Cron: "@daily", Task: "follow up", Source: SourceLLM, Enabled: true, Created: time.Now()}
	if err := store.Set(ctx, llmSched); err != nil {
		t.Fatalf("seed LLM schedule: %v", err)
	}

	// Sync with one yaml entry that is NOT the LLM-sourced one.
	if err := b.Sync(ctx, []Schedule{
		{ID: "yaml-1", Cron: "@hourly", Task: "t", Source: SourceYAML, Enabled: true, Created: time.Now()},
	}); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if _, exists := store.schedules["from-chat"]; !exists {
		t.Errorf("LLM-sourced schedule was incorrectly pruned by Sync")
	}
	if _, exists := store.schedules["yaml-1"]; !exists {
		t.Errorf("yaml-sourced entry was not added by Sync")
	}
}

// TestFileBackend_SyncPreservesPerRunState verifies that re-Sync of
// the same yaml entry doesn't reset LastRun / RunCount counters. The
// operator editing the task description in forge.yaml shouldn't lose
// the execution history.
func TestFileBackend_SyncPreservesPerRunState(t *testing.T) {
	b, store := newTestBackend(t)
	ctx := context.Background()

	// First Sync establishes the entry.
	created := time.Now().Add(-1 * time.Hour)
	if err := b.Sync(ctx, []Schedule{
		{ID: "x", Cron: "@hourly", Task: "old task text", Source: SourceYAML, Enabled: true, Created: created},
	}); err != nil {
		t.Fatalf("Sync first: %v", err)
	}
	// Simulate runtime state being updated by a Scheduler fire.
	cur := store.schedules["x"]
	cur.LastRun = time.Now().Add(-5 * time.Minute)
	cur.LastStatus = "completed"
	cur.RunCount = 42
	store.schedules["x"] = cur

	// Re-Sync with updated task text but same ID.
	if err := b.Sync(ctx, []Schedule{
		{ID: "x", Cron: "@hourly", Task: "new task text", Source: SourceYAML, Enabled: true, Created: time.Now()},
	}); err != nil {
		t.Fatalf("Sync second: %v", err)
	}

	got := store.schedules["x"]
	if got.Task != "new task text" {
		t.Errorf("task text should update: got %q", got.Task)
	}
	if got.RunCount != 42 {
		t.Errorf("RunCount should survive Sync: got %d, want 42", got.RunCount)
	}
	if got.LastStatus != "completed" {
		t.Errorf("LastStatus should survive Sync: got %q", got.LastStatus)
	}
}

// TestFileBackend_StoreAccessor confirms the Store() escape hatch.
// The existing schedule_* builtin tools call into ScheduleStore via
// runtime reload-aware adapters; exposing the same store via
// Backend.Store() keeps that call path working in both modes (the
// kubernetes backend will return an adapter that translates
// ScheduleStore methods to CronJob API calls).
func TestFileBackend_StoreAccessor(t *testing.T) {
	b, store := newTestBackend(t)
	if b.Store() != store {
		t.Errorf("Store() must return the same ScheduleStore instance the backend was constructed with")
	}
}
