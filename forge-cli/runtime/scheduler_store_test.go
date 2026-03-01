package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/scheduler"
)

func testStore(t *testing.T) (*MemoryScheduleStore, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".forge", "memory", "SCHEDULES.md")
	return NewMemoryScheduleStore(path), path
}

func TestMemoryScheduleStore_RoundTrip(t *testing.T) {
	store, path := testStore(t)
	ctx := context.Background()

	// Initially empty.
	schedules, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(schedules) != 0 {
		t.Fatalf("expected 0 schedules, got %d", len(schedules))
	}

	// Set a schedule.
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sched := scheduler.Schedule{
		ID:      "test-health-check",
		Cron:    "*/15 * * * *",
		Task:    "Run health check on all services",
		Skill:   "k8s-health",
		Source:  "llm",
		Enabled: true,
		Created: created,
	}
	if err := store.Set(ctx, sched); err != nil {
		t.Fatal(err)
	}

	// Verify file was created.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created: %v", err)
	}

	// List should return it.
	schedules, err = store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(schedules) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(schedules))
	}
	if schedules[0].ID != "test-health-check" {
		t.Fatalf("unexpected ID: %s", schedules[0].ID)
	}
	if schedules[0].Cron != "*/15 * * * *" {
		t.Fatalf("unexpected Cron: %s", schedules[0].Cron)
	}
	if !schedules[0].Enabled {
		t.Fatal("expected enabled")
	}

	// Get by ID.
	got, err := store.Get(ctx, "test-health-check")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected schedule, got nil")
	}
	if got.Task != "Run health check on all services" {
		t.Fatalf("unexpected Task: %s", got.Task)
	}

	// Get non-existent.
	got, err = store.Get(ctx, "nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatal("expected nil for nonexistent schedule")
	}

	// Update.
	sched.Enabled = false
	sched.RunCount = 5
	if err := store.Set(ctx, sched); err != nil {
		t.Fatal(err)
	}

	got, _ = store.Get(ctx, "test-health-check")
	if got.Enabled {
		t.Fatal("expected disabled after update")
	}
	if got.RunCount != 5 {
		t.Fatalf("expected RunCount 5, got %d", got.RunCount)
	}

	// Delete.
	if err := store.Delete(ctx, "test-health-check"); err != nil {
		t.Fatal(err)
	}
	schedules, _ = store.List(ctx)
	if len(schedules) != 0 {
		t.Fatalf("expected 0 after delete, got %d", len(schedules))
	}
}

func TestMemoryScheduleStore_History(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()

	// Need a schedule first.
	_ = store.Set(ctx, scheduler.Schedule{
		ID:      "h-1",
		Cron:    "* * * * *",
		Task:    "test",
		Source:  "llm",
		Enabled: true,
		Created: time.Now().UTC(),
	})

	// Record some history.
	for i := range 5 {
		_ = store.RecordRun(ctx, scheduler.HistoryEntry{
			Timestamp:     time.Date(2026, 1, 1, 0, i, 0, 0, time.UTC),
			ScheduleID:    "h-1",
			Status:        "completed",
			Duration:      "1.0s",
			CorrelationID: "abc123",
		})
	}

	// Get all history.
	history, err := store.History(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 5 {
		t.Fatalf("expected 5 history entries, got %d", len(history))
	}

	// Filter by schedule ID.
	history, _ = store.History(ctx, "h-1", 10)
	if len(history) != 5 {
		t.Fatalf("expected 5 filtered entries, got %d", len(history))
	}

	// Filter by nonexistent ID.
	history, _ = store.History(ctx, "nonexistent", 10)
	if len(history) != 0 {
		t.Fatalf("expected 0 for nonexistent filter, got %d", len(history))
	}

	// Limit.
	history, _ = store.History(ctx, "", 3)
	if len(history) != 3 {
		t.Fatalf("expected 3 limited entries, got %d", len(history))
	}
}

func TestMemoryScheduleStore_HistoryPruning(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()

	_ = store.Set(ctx, scheduler.Schedule{
		ID:      "prune-1",
		Cron:    "* * * * *",
		Task:    "test",
		Source:  "llm",
		Enabled: true,
		Created: time.Now().UTC(),
	})

	// Record more than maxHistory entries.
	for i := range 60 {
		_ = store.RecordRun(ctx, scheduler.HistoryEntry{
			Timestamp:     time.Date(2026, 1, 1, i/60, i%60, 0, 0, time.UTC),
			ScheduleID:    "prune-1",
			Status:        "completed",
			Duration:      "0.5s",
			CorrelationID: "x",
		})
	}

	history, _ := store.History(ctx, "", 100)
	if len(history) != maxHistory {
		t.Fatalf("expected %d history entries after pruning, got %d", maxHistory, len(history))
	}
}

func TestMemoryScheduleStore_ChannelFieldsRoundTrip(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()

	sched := scheduler.Schedule{
		ID:            "notify-test",
		Cron:          "@hourly",
		Task:          "Send hourly update",
		Channel:       "telegram",
		ChannelTarget: "-100123456",
		Source:        "llm",
		Enabled:       true,
		Created:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := store.Set(ctx, sched); err != nil {
		t.Fatal(err)
	}

	got, err := store.Get(ctx, "notify-test")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected schedule, got nil")
	}
	if got.Channel != "telegram" {
		t.Fatalf("Channel: got %q, want %q", got.Channel, "telegram")
	}
	if got.ChannelTarget != "-100123456" {
		t.Fatalf("ChannelTarget: got %q, want %q", got.ChannelTarget, "-100123456")
	}
}

func TestMemoryScheduleStore_MissingFileCreation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deep", "nested", "dir", "SCHEDULES.md")
	store := NewMemoryScheduleStore(path)
	ctx := context.Background()

	// Setting should auto-create the directory.
	err := store.Set(ctx, scheduler.Schedule{
		ID:      "auto-create",
		Cron:    "0 0 * * *",
		Task:    "test",
		Source:  "llm",
		Enabled: true,
		Created: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Verify file exists.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created: %v", err)
	}
}

func TestMemoryScheduleStore_Concurrent(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			sched := scheduler.Schedule{
				ID:      fmt.Sprintf("concurrent-%d", n),
				Cron:    "0 * * * *",
				Task:    "test",
				Source:  "llm",
				Enabled: true,
				Created: time.Now().UTC(),
			}
			_ = store.Set(ctx, sched)
			_, _ = store.List(ctx)
		}(i)
	}
	wg.Wait()

	// All should be present.
	schedules, _ := store.List(ctx)
	if len(schedules) != 10 {
		t.Fatalf("expected 10 schedules after concurrent writes, got %d", len(schedules))
	}
}
