package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/initializ/forge/forge-core/scheduler"
)

const (
	schedulesHeader  = "# Forge Schedules\n"
	historyHeader    = "## History\n"
	historyTableHead = "| Timestamp | Schedule ID | Status | Duration | Correlation ID | Error |\n| --- | --- | --- | --- | --- | --- |\n"
	maxHistory       = 50
)

// MemoryScheduleStore implements scheduler.ScheduleStore backed by a markdown file.
type MemoryScheduleStore struct {
	mu   sync.RWMutex
	path string
}

// NewMemoryScheduleStore creates a store at the given file path.
func NewMemoryScheduleStore(path string) *MemoryScheduleStore {
	return &MemoryScheduleStore{path: path}
}

func (s *MemoryScheduleStore) List(_ context.Context) ([]scheduler.Schedule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	schedules, _, err := s.readFile()
	return schedules, err
}

func (s *MemoryScheduleStore) Get(_ context.Context, id string) (*scheduler.Schedule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	schedules, _, err := s.readFile()
	if err != nil {
		return nil, err
	}
	for _, sched := range schedules {
		if sched.ID == id {
			return &sched, nil
		}
	}
	return nil, nil
}

func (s *MemoryScheduleStore) Set(_ context.Context, sched scheduler.Schedule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	schedules, history, err := s.readFile()
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	// Upsert.
	found := false
	for i, existing := range schedules {
		if existing.ID == sched.ID {
			schedules[i] = sched
			found = true
			break
		}
	}
	if !found {
		schedules = append(schedules, sched)
	}

	return s.writeFile(schedules, history)
}

func (s *MemoryScheduleStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	schedules, history, err := s.readFile()
	if err != nil {
		return err
	}

	var filtered []scheduler.Schedule
	for _, sched := range schedules {
		if sched.ID != id {
			filtered = append(filtered, sched)
		}
	}

	return s.writeFile(filtered, history)
}

func (s *MemoryScheduleStore) RecordRun(_ context.Context, entry scheduler.HistoryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	schedules, history, err := s.readFile()
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	history = append(history, entry)

	// Prune to last maxHistory entries.
	if len(history) > maxHistory {
		history = history[len(history)-maxHistory:]
	}

	return s.writeFile(schedules, history)
}

func (s *MemoryScheduleStore) History(_ context.Context, scheduleID string, limit int) ([]scheduler.HistoryEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, history, err := s.readFile()
	if err != nil {
		return nil, err
	}

	if scheduleID != "" {
		var filtered []scheduler.HistoryEntry
		for _, h := range history {
			if h.ScheduleID == scheduleID {
				filtered = append(filtered, h)
			}
		}
		history = filtered
	}

	if limit > 0 && len(history) > limit {
		history = history[len(history)-limit:]
	}

	return history, nil
}

// readFile parses the SCHEDULES.md file.
func (s *MemoryScheduleStore) readFile() ([]scheduler.Schedule, []scheduler.HistoryEntry, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	return parseSchedulesMD(string(data))
}

// writeFile atomically writes the SCHEDULES.md file.
func (s *MemoryScheduleStore) writeFile(schedules []scheduler.Schedule, history []scheduler.HistoryEntry) error {
	// Ensure directory exists.
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating directory %s: %w", dir, err)
	}

	content := renderSchedulesMD(schedules, history)

	// Atomic write: temp file → fsync → rename.
	tmp := s.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}

	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("writing temp file: %w", err)
	}

	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("syncing temp file: %w", err)
	}
	_ = f.Close()

	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming temp file: %w", err)
	}

	return nil
}

// parseSchedulesMD parses the markdown content into schedules and history.
func parseSchedulesMD(content string) ([]scheduler.Schedule, []scheduler.HistoryEntry, error) {
	var schedules []scheduler.Schedule
	var history []scheduler.HistoryEntry

	lines := strings.Split(content, "\n")
	var current *scheduler.Schedule
	inHistory := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Schedule block start.
		if strings.HasPrefix(trimmed, "## Schedule:") {
			if current != nil {
				schedules = append(schedules, *current)
			}
			inHistory = false
			current = &scheduler.Schedule{}
			continue
		}

		// History section start.
		if trimmed == "## History" {
			if current != nil {
				schedules = append(schedules, *current)
				current = nil
			}
			inHistory = true
			continue
		}

		// Parse schedule fields.
		if current != nil && strings.HasPrefix(trimmed, "- **") {
			parseScheduleField(current, trimmed)
			continue
		}

		// Parse history table rows.
		if inHistory && strings.HasPrefix(trimmed, "|") && !strings.Contains(trimmed, "---") && !strings.HasPrefix(trimmed, "| Timestamp") {
			entry, err := parseHistoryRow(trimmed)
			if err == nil {
				history = append(history, entry)
			}
		}
	}

	// Don't forget last schedule block.
	if current != nil {
		schedules = append(schedules, *current)
	}

	return schedules, history, nil
}

func parseScheduleField(sched *scheduler.Schedule, line string) {
	// Format: "- **Key:** Value"
	line = strings.TrimPrefix(line, "- **")
	idx := strings.Index(line, ":** ")
	if idx < 0 {
		return
	}
	key := line[:idx]
	value := strings.TrimSpace(line[idx+4:])

	switch key {
	case "ID":
		sched.ID = value
	case "Cron":
		sched.Cron = value
	case "Task":
		sched.Task = value
	case "Skill":
		sched.Skill = value
	case "Channel":
		sched.Channel = value
	case "Channel Target":
		sched.ChannelTarget = value
	case "Source":
		sched.Source = value
	case "Enabled":
		sched.Enabled = value == "true"
	case "Created":
		t, err := time.Parse(time.RFC3339, value)
		if err == nil {
			sched.Created = t
		}
	case "Last Run":
		if value != "" && value != "never" {
			t, err := time.Parse(time.RFC3339, value)
			if err == nil {
				sched.LastRun = t
			}
		}
	case "Last Status":
		sched.LastStatus = value
	case "Run Count":
		n, err := strconv.Atoi(value)
		if err == nil {
			sched.RunCount = n
		}
	}
}

func parseHistoryRow(line string) (scheduler.HistoryEntry, error) {
	// Format: "| Timestamp | Schedule ID | Status | Duration | Correlation ID | Error |"
	parts := strings.Split(line, "|")
	// Leading and trailing empty strings from split.
	var fields []string
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			fields = append(fields, trimmed)
		}
	}

	if len(fields) < 5 {
		return scheduler.HistoryEntry{}, fmt.Errorf("not enough fields")
	}

	ts, err := time.Parse(time.RFC3339, fields[0])
	if err != nil {
		return scheduler.HistoryEntry{}, err
	}

	entry := scheduler.HistoryEntry{
		Timestamp:     ts,
		ScheduleID:    fields[1],
		Status:        fields[2],
		Duration:      fields[3],
		CorrelationID: fields[4],
	}

	if len(fields) > 5 {
		entry.Error = fields[5]
	}

	return entry, nil
}

// renderSchedulesMD renders schedules and history to markdown.
func renderSchedulesMD(schedules []scheduler.Schedule, history []scheduler.HistoryEntry) string {
	var b strings.Builder
	b.WriteString(schedulesHeader)
	b.WriteString("\n")

	for _, sched := range schedules {
		fmt.Fprintf(&b, "## Schedule: %s\n\n", sched.ID)
		fmt.Fprintf(&b, "- **ID:** %s\n", sched.ID)
		fmt.Fprintf(&b, "- **Cron:** %s\n", sched.Cron)
		fmt.Fprintf(&b, "- **Task:** %s\n", sched.Task)
		if sched.Skill != "" {
			fmt.Fprintf(&b, "- **Skill:** %s\n", sched.Skill)
		}
		if sched.Channel != "" {
			fmt.Fprintf(&b, "- **Channel:** %s\n", sched.Channel)
		}
		if sched.ChannelTarget != "" {
			fmt.Fprintf(&b, "- **Channel Target:** %s\n", sched.ChannelTarget)
		}
		fmt.Fprintf(&b, "- **Source:** %s\n", sched.Source)
		fmt.Fprintf(&b, "- **Enabled:** %t\n", sched.Enabled)
		fmt.Fprintf(&b, "- **Created:** %s\n", sched.Created.Format(time.RFC3339))

		if sched.LastRun.IsZero() {
			b.WriteString("- **Last Run:** never\n")
		} else {
			fmt.Fprintf(&b, "- **Last Run:** %s\n", sched.LastRun.Format(time.RFC3339))
		}

		fmt.Fprintf(&b, "- **Last Status:** %s\n", sched.LastStatus)
		fmt.Fprintf(&b, "- **Run Count:** %d\n", sched.RunCount)
		b.WriteString("\n")
	}

	b.WriteString(historyHeader)
	b.WriteString("\n")
	b.WriteString(historyTableHead)

	for _, h := range history {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n",
			h.Timestamp.Format(time.RFC3339),
			h.ScheduleID,
			h.Status,
			h.Duration,
			h.CorrelationID,
			h.Error,
		)
	}

	return b.String()
}
