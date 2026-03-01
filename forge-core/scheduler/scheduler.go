package scheduler

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Logger is the minimal logging interface used by the scheduler.
type Logger interface {
	Info(msg string, fields map[string]any)
	Warn(msg string, fields map[string]any)
	Error(msg string, fields map[string]any)
}

// AuditFunc is a function that emits audit events. Nil means no auditing.
type AuditFunc func(event, scheduleID string, fields map[string]any)

// Audit event constants for schedule operations.
const (
	AuditScheduleFire     = "schedule_fire"
	AuditScheduleComplete = "schedule_complete"
	AuditScheduleSkip     = "schedule_skip"
	AuditScheduleModify   = "schedule_modify"
)

// Scheduler runs scheduled tasks on a tick loop.
type Scheduler struct {
	store    ScheduleStore
	dispatch TaskDispatcher
	logger   Logger
	audit    AuditFunc

	mu      sync.Mutex
	running map[string]bool           // overlap prevention
	parsed  map[string]ParsedSchedule // cache

	stopCh chan struct{}
	done   chan struct{}
}

// New creates a new Scheduler.
func New(store ScheduleStore, dispatch TaskDispatcher, logger Logger, audit AuditFunc) *Scheduler {
	return &Scheduler{
		store:    store,
		dispatch: dispatch,
		logger:   logger,
		audit:    audit,
		running:  make(map[string]bool),
		parsed:   make(map[string]ParsedSchedule),
		stopCh:   make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Start launches the scheduler tick loop. It blocks until Stop is called.
func (s *Scheduler) Start(ctx context.Context) {
	go s.loop(ctx)
}

// Stop signals the scheduler to stop and waits for it to exit.
func (s *Scheduler) Stop() {
	close(s.stopCh)
	<-s.done
}

// Reload re-reads the store and recomputes parsed expressions.
func (s *Scheduler) Reload(ctx context.Context) {
	schedules, err := s.store.List(ctx)
	if err != nil {
		s.logger.Warn("scheduler reload: failed to list schedules", map[string]any{"error": err.Error()})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.parsed = make(map[string]ParsedSchedule, len(schedules))
	for _, sched := range schedules {
		if !sched.Enabled {
			continue
		}
		ps, parseErr := Parse(sched.Cron)
		if parseErr != nil {
			s.logger.Warn("scheduler reload: invalid cron expression", map[string]any{
				"id": sched.ID, "cron": sched.Cron, "error": parseErr.Error(),
			})
			continue
		}
		s.parsed[sched.ID] = ps
	}

	s.logger.Info("scheduler reloaded", map[string]any{"active": len(s.parsed)})
}

func (s *Scheduler) loop(ctx context.Context) {
	defer close(s.done)

	// Initial load.
	s.Reload(ctx)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	schedules, err := s.store.List(ctx)
	if err != nil {
		s.logger.Warn("scheduler tick: failed to list schedules", map[string]any{"error": err.Error()})
		return
	}

	now := time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, sched := range schedules {
		if !sched.Enabled {
			continue
		}

		ps, ok := s.parsed[sched.ID]
		if !ok {
			// Parse on demand if not cached (e.g., newly added).
			var parseErr error
			ps, parseErr = Parse(sched.Cron)
			if parseErr != nil {
				continue
			}
			s.parsed[sched.ID] = ps
		}

		// Compute next fire time based on last run.
		ref := sched.LastRun
		if ref.IsZero() {
			ref = sched.Created
		}

		next := ps.Next(ref)
		if next.After(now) {
			continue // Not due yet.
		}

		// Check overlap.
		if s.running[sched.ID] {
			s.logger.Info("schedule skipped (overlap)", map[string]any{"id": sched.ID})
			if s.audit != nil {
				s.audit(AuditScheduleSkip, sched.ID, map[string]any{"reason": "overlap"})
			}
			_ = s.store.RecordRun(ctx, HistoryEntry{
				Timestamp:  now,
				ScheduleID: sched.ID,
				Status:     "skipped",
			})
			continue
		}

		// Fire the schedule.
		s.running[sched.ID] = true
		schedCopy := sched
		go s.fire(ctx, schedCopy, now)
	}
}

func (s *Scheduler) fire(ctx context.Context, sched Schedule, fireTime time.Time) {
	start := time.Now()

	if s.audit != nil {
		s.audit(AuditScheduleFire, sched.ID, map[string]any{"task": sched.Task})
	}

	s.logger.Info("firing scheduled task", map[string]any{
		"id":   sched.ID,
		"task": sched.Task,
	})

	err := s.dispatch(ctx, sched)
	duration := time.Since(start)

	status := "completed"
	errStr := ""
	if err != nil {
		status = "error"
		errStr = err.Error()
		s.logger.Error("scheduled task failed", map[string]any{
			"id": sched.ID, "error": err.Error(), "duration": duration.String(),
		})
	} else {
		s.logger.Info("scheduled task completed", map[string]any{
			"id": sched.ID, "duration": duration.String(),
		})
	}

	if s.audit != nil {
		s.audit(AuditScheduleComplete, sched.ID, map[string]any{
			"status":   status,
			"duration": duration.String(),
		})
	}

	// Update schedule state.
	sched.LastRun = fireTime
	sched.LastStatus = status
	sched.RunCount++
	if setErr := s.store.Set(ctx, sched); setErr != nil {
		s.logger.Warn("failed to update schedule after run", map[string]any{
			"id": sched.ID, "error": setErr.Error(),
		})
	}

	// Record history.
	_ = s.store.RecordRun(ctx, HistoryEntry{
		Timestamp:  fireTime,
		ScheduleID: sched.ID,
		Status:     status,
		Duration:   fmt.Sprintf("%.1fs", duration.Seconds()),
		Error:      errStr,
	})

	// Clear running flag.
	s.mu.Lock()
	delete(s.running, sched.ID)
	s.mu.Unlock()
}
