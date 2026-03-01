// Package scheduler provides a cron-based task scheduler for Forge agents.
package scheduler

import (
	"context"
	"time"
)

// Schedule represents a recurring scheduled task.
type Schedule struct {
	ID            string    `json:"id"`
	Cron          string    `json:"cron"`
	Task          string    `json:"task"`
	Skill         string    `json:"skill,omitempty"`
	Channel       string    `json:"channel,omitempty"`        // channel adapter name (e.g. "slack", "telegram")
	ChannelTarget string    `json:"channel_target,omitempty"` // destination ID (channel ID, chat ID)
	Source        string    `json:"source"`                   // "yaml" or "llm"
	Enabled       bool      `json:"enabled"`
	Created       time.Time `json:"created"`
	LastRun       time.Time `json:"last_run,omitempty"`
	LastStatus    string    `json:"last_status,omitempty"` // completed, error, running, skipped
	RunCount      int       `json:"run_count"`
}

// HistoryEntry records a single execution of a scheduled task.
type HistoryEntry struct {
	Timestamp     time.Time `json:"timestamp"`
	ScheduleID    string    `json:"schedule_id"`
	Status        string    `json:"status"` // completed, error, skipped
	Duration      string    `json:"duration"`
	CorrelationID string    `json:"correlation_id"`
	Error         string    `json:"error,omitempty"`
}

// TaskDispatcher is the function signature used to execute a scheduled task.
type TaskDispatcher func(ctx context.Context, sched Schedule) error
