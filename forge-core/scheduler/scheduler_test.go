package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"
)

// mockStore implements ScheduleStore for testing.
type mockStore struct {
	mu        sync.Mutex
	schedules map[string]Schedule
	history   []HistoryEntry
}

func newMockStore() *mockStore {
	return &mockStore{schedules: make(map[string]Schedule)}
}

func (m *mockStore) List(_ context.Context) ([]Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Schedule
	for _, s := range m.schedules {
		out = append(out, s)
	}
	return out, nil
}

func (m *mockStore) Get(_ context.Context, id string) (*Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.schedules[id]
	if !ok {
		return nil, nil
	}
	return &s, nil
}

func (m *mockStore) Set(_ context.Context, sched Schedule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.schedules[sched.ID] = sched
	return nil
}

func (m *mockStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.schedules, id)
	return nil
}

func (m *mockStore) RecordRun(_ context.Context, entry HistoryEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.history = append(m.history, entry)
	return nil
}

func (m *mockStore) History(_ context.Context, scheduleID string, limit int) ([]HistoryEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []HistoryEntry
	for _, h := range m.history {
		if scheduleID != "" && h.ScheduleID != scheduleID {
			continue
		}
		out = append(out, h)
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

// mockLogger implements Logger for testing.
type mockLogger struct{}

func (m *mockLogger) Info(_ string, _ map[string]any)  {}
func (m *mockLogger) Warn(_ string, _ map[string]any)  {}
func (m *mockLogger) Error(_ string, _ map[string]any) {}

func TestScheduler_SingleFire(t *testing.T) {
	store := newMockStore()
	var fired []string
	var mu sync.Mutex
	dispatch := func(_ context.Context, sched Schedule) error {
		mu.Lock()
		fired = append(fired, sched.ID)
		mu.Unlock()
		return nil
	}

	// Schedule that was due 5 minutes ago.
	store.schedules["test-1"] = Schedule{
		ID:      "test-1",
		Cron:    "* * * * *",
		Task:    "do something",
		Source:  "llm",
		Enabled: true,
		Created: time.Now().UTC().Add(-10 * time.Minute),
		LastRun: time.Now().UTC().Add(-5 * time.Minute),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sched := New(store, dispatch, &mockLogger{}, nil)
	sched.Reload(ctx)
	sched.tick(ctx)

	// Wait for goroutine to complete.
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(fired) != 1 || fired[0] != "test-1" {
		t.Fatalf("expected [test-1] to fire, got %v", fired)
	}
}

func TestScheduler_DisabledSchedule(t *testing.T) {
	store := newMockStore()
	var fired []string
	dispatch := func(_ context.Context, sched Schedule) error {
		fired = append(fired, sched.ID)
		return nil
	}

	store.schedules["disabled-1"] = Schedule{
		ID:      "disabled-1",
		Cron:    "* * * * *",
		Task:    "do something",
		Source:  "llm",
		Enabled: false,
		Created: time.Now().UTC().Add(-10 * time.Minute),
	}

	ctx := context.Background()
	sched := New(store, dispatch, &mockLogger{}, nil)
	sched.Reload(ctx)
	sched.tick(ctx)

	time.Sleep(50 * time.Millisecond)

	if len(fired) != 0 {
		t.Fatalf("disabled schedule should not fire, got %v", fired)
	}
}

func TestScheduler_OverlapSkip(t *testing.T) {
	store := newMockStore()
	blockCh := make(chan struct{})
	var fireCount int
	var mu sync.Mutex

	dispatch := func(_ context.Context, sched Schedule) error {
		mu.Lock()
		fireCount++
		mu.Unlock()
		<-blockCh // Block until released
		return nil
	}

	store.schedules["overlap-1"] = Schedule{
		ID:      "overlap-1",
		Cron:    "* * * * *",
		Task:    "slow task",
		Source:  "llm",
		Enabled: true,
		Created: time.Now().UTC().Add(-10 * time.Minute),
		LastRun: time.Now().UTC().Add(-5 * time.Minute),
	}

	ctx := context.Background()
	sched := New(store, dispatch, &mockLogger{}, nil)
	sched.Reload(ctx)

	// First tick fires.
	sched.tick(ctx)
	time.Sleep(50 * time.Millisecond)

	// Second tick should skip (still running).
	sched.tick(ctx)
	time.Sleep(50 * time.Millisecond)

	// Release the blocked dispatch.
	close(blockCh)
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if fireCount != 1 {
		t.Fatalf("expected 1 fire (overlap should skip), got %d", fireCount)
	}

	// Should have a "skipped" history entry.
	history, _ := store.History(ctx, "overlap-1", 10)
	var skipped int
	for _, h := range history {
		if h.Status == "skipped" {
			skipped++
		}
	}
	if skipped != 1 {
		t.Fatalf("expected 1 skipped history entry, got %d", skipped)
	}
}

func TestScheduler_Reload(t *testing.T) {
	store := newMockStore()
	dispatch := func(_ context.Context, sched Schedule) error { return nil }

	ctx := context.Background()
	sched := New(store, dispatch, &mockLogger{}, nil)
	sched.Reload(ctx)

	sched.mu.Lock()
	if len(sched.parsed) != 0 {
		t.Fatalf("expected 0 parsed schedules, got %d", len(sched.parsed))
	}
	sched.mu.Unlock()

	// Add a schedule and reload.
	store.schedules["new-1"] = Schedule{
		ID:      "new-1",
		Cron:    "0 * * * *",
		Task:    "hourly check",
		Source:  "llm",
		Enabled: true,
		Created: time.Now().UTC(),
	}

	sched.Reload(ctx)

	sched.mu.Lock()
	defer sched.mu.Unlock()
	if len(sched.parsed) != 1 {
		t.Fatalf("expected 1 parsed schedule after reload, got %d", len(sched.parsed))
	}
}

func TestScheduler_StartStop(t *testing.T) {
	store := newMockStore()
	dispatch := func(_ context.Context, sched Schedule) error { return nil }

	ctx := context.Background()
	sched := New(store, dispatch, &mockLogger{}, nil)
	sched.Start(ctx)

	// Should stop cleanly within a reasonable time.
	done := make(chan struct{})
	go func() {
		sched.Stop()
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return in time")
	}
}
