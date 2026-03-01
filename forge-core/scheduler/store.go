package scheduler

import "context"

// ScheduleStore defines the persistence interface for schedules and their history.
type ScheduleStore interface {
	// List returns all schedules.
	List(ctx context.Context) ([]Schedule, error)
	// Get returns a single schedule by ID, or nil if not found.
	Get(ctx context.Context, id string) (*Schedule, error)
	// Set creates or updates a schedule.
	Set(ctx context.Context, sched Schedule) error
	// Delete removes a schedule by ID.
	Delete(ctx context.Context, id string) error
	// RecordRun records a history entry for a completed schedule execution.
	RecordRun(ctx context.Context, entry HistoryEntry) error
	// History returns recent history entries, optionally filtered by schedule ID.
	History(ctx context.Context, scheduleID string, limit int) ([]HistoryEntry, error)
}
