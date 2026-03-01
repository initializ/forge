package builtins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/initializ/forge/forge-core/scheduler"
	"github.com/initializ/forge/forge-core/tools"
)

// ScheduleReloader is the interface for reloading the scheduler after changes.
type ScheduleReloader interface {
	Reload(ctx context.Context)
}

type scheduleSetTool struct {
	store    scheduler.ScheduleStore
	reloader ScheduleReloader
}

// NewScheduleSetTool creates a schedule_set tool for creating/updating schedules.
func NewScheduleSetTool(store scheduler.ScheduleStore, reloader ScheduleReloader) tools.Tool {
	return &scheduleSetTool{store: store, reloader: reloader}
}

type scheduleSetInput struct {
	ID            string `json:"id"`
	Cron          string `json:"cron"`
	Task          string `json:"task"`
	Skill         string `json:"skill"`
	Channel       string `json:"channel"`
	ChannelTarget string `json:"channel_target"`
	Enabled       *bool  `json:"enabled"`
}

func (t *scheduleSetTool) Name() string             { return "schedule_set" }
func (t *scheduleSetTool) Category() tools.Category { return tools.CategoryBuiltin }
func (t *scheduleSetTool) Description() string {
	return "Create or update a recurring scheduled task. Supports standard 5-field cron expressions (e.g. '*/15 * * * *'), aliases (@hourly, @daily, @weekly, @monthly), and intervals (@every 5m)."
}

func (t *scheduleSetTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {"type": "string", "description": "Schedule ID (auto-generated from task if omitted). Must be kebab-case."},
			"cron": {"type": "string", "description": "Cron expression: 5-field (min hour dom mon dow), @hourly/@daily/@weekly/@monthly, or @every <duration>"},
			"task": {"type": "string", "description": "The task description to execute on each trigger"},
			"skill": {"type": "string", "description": "Optional skill name to invoke"},
			"channel": {"type": "string", "description": "Channel adapter to send results to (e.g. slack, telegram). Required for schedule results to be delivered to a channel."},
			"channel_target": {"type": "string", "description": "Destination ID for the channel (Slack channel ID, Telegram chat ID). Required when channel is set."},
			"enabled": {"type": "boolean", "description": "Whether the schedule is active (default: true)"}
		},
		"required": ["cron", "task"]
	}`)
}

var kebabPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func (t *scheduleSetTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input scheduleSetInput
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("parsing input: %w", err)
	}

	if input.Cron == "" {
		return "", fmt.Errorf("cron is required")
	}
	if input.Task == "" {
		return "", fmt.Errorf("task is required")
	}

	// Validate cron expression.
	parsed, err := scheduler.Parse(input.Cron)
	if err != nil {
		return "", fmt.Errorf("invalid cron expression: %w", err)
	}

	// Auto-generate ID if not provided.
	id := input.ID
	if id == "" {
		id = slugify(input.Task)
	}
	if !kebabPattern.MatchString(id) {
		return "", fmt.Errorf("schedule ID %q must be kebab-case (lowercase letters, numbers, hyphens)", id)
	}

	// Check if modifying a yaml-sourced schedule.
	existing, err := t.store.Get(ctx, id)
	if err != nil {
		return "", fmt.Errorf("checking existing schedule: %w", err)
	}
	if existing != nil && existing.Source == "yaml" {
		return "", fmt.Errorf("cannot modify schedule %q: it is defined in forge.yaml (source: yaml)", id)
	}

	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	} else if existing != nil {
		enabled = existing.Enabled
	}

	now := time.Now().UTC()
	sched := scheduler.Schedule{
		ID:            id,
		Cron:          input.Cron,
		Task:          input.Task,
		Skill:         input.Skill,
		Channel:       input.Channel,
		ChannelTarget: input.ChannelTarget,
		Source:        "llm",
		Enabled:       enabled,
		Created:       now,
	}

	// Preserve fields from existing schedule.
	if existing != nil {
		sched.Created = existing.Created
		sched.LastRun = existing.LastRun
		sched.LastStatus = existing.LastStatus
		sched.RunCount = existing.RunCount
		// Preserve channel settings if not explicitly set in this update.
		if sched.Channel == "" {
			sched.Channel = existing.Channel
			sched.ChannelTarget = existing.ChannelTarget
		}
	}

	if err := t.store.Set(ctx, sched); err != nil {
		return "", fmt.Errorf("saving schedule: %w", err)
	}

	// Reload scheduler to pick up changes.
	t.reloader.Reload(ctx)

	next := parsed.Next(now)
	action := "Created"
	if existing != nil {
		action = "Updated"
	}

	return fmt.Sprintf("%s schedule %q.\nCron: %s\nTask: %s\nNext fire: %s",
		action, id, input.Cron, input.Task, next.Format(time.RFC3339)), nil
}

// slugify converts a task description into a kebab-case ID.
// Takes first 5 words, lowercases, removes non-alphanumeric, appends 4-char hash.
func slugify(task string) string {
	words := strings.Fields(strings.ToLower(task))
	if len(words) > 5 {
		words = words[:5]
	}

	var cleaned []string
	for _, w := range words {
		var b strings.Builder
		for _, r := range w {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
			}
		}
		if b.Len() > 0 {
			cleaned = append(cleaned, b.String())
		}
	}

	slug := strings.Join(cleaned, "-")
	if slug == "" {
		slug = "schedule"
	}

	// Append 4-char hash for uniqueness.
	h := sha256.Sum256([]byte(task))
	slug += "-" + hex.EncodeToString(h[:2])
	return slug
}
