package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParsedSchedule computes the next fire time after a given reference.
type ParsedSchedule interface {
	Next(after time.Time) time.Time
}

// Parse parses a cron expression and returns a ParsedSchedule.
// Supported formats:
//   - Standard 5-field: "minute hour dom month dow"
//   - Aliases: @hourly, @daily, @weekly, @monthly
//   - Intervals: @every 5m, @every 1h30m
func Parse(expr string) (ParsedSchedule, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, fmt.Errorf("empty cron expression")
	}

	// Handle aliases.
	switch expr {
	case "@hourly":
		return Parse("0 * * * *")
	case "@daily":
		return Parse("0 0 * * *")
	case "@weekly":
		return Parse("0 0 * * 0")
	case "@monthly":
		return Parse("0 0 1 * *")
	}

	// Handle @every intervals.
	if strings.HasPrefix(expr, "@every ") {
		durStr := strings.TrimPrefix(expr, "@every ")
		dur, err := time.ParseDuration(durStr)
		if err != nil {
			return nil, fmt.Errorf("invalid interval %q: %w", durStr, err)
		}
		if dur < time.Minute {
			return nil, fmt.Errorf("minimum interval is 1 minute, got %s", dur)
		}
		return &IntervalSchedule{Interval: dur}, nil
	}

	// Standard 5-field cron.
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("expected 5 fields, got %d in %q", len(fields), expr)
	}

	cs := &CronSchedule{}
	var err error

	cs.Minute, err = parseField(fields[0], 0, 59)
	if err != nil {
		return nil, fmt.Errorf("minute field: %w", err)
	}
	cs.Hour, err = parseField(fields[1], 0, 23)
	if err != nil {
		return nil, fmt.Errorf("hour field: %w", err)
	}
	cs.Dom, err = parseField(fields[2], 1, 31)
	if err != nil {
		return nil, fmt.Errorf("day-of-month field: %w", err)
	}
	cs.Month, err = parseField(fields[3], 1, 12)
	if err != nil {
		return nil, fmt.Errorf("month field: %w", err)
	}
	cs.Dow, err = parseField(fields[4], 0, 6)
	if err != nil {
		return nil, fmt.Errorf("day-of-week field: %w", err)
	}

	// Enforce minimum 1-minute interval: reject "* * * * *" (every minute is allowed,
	// but we validate the minimum on @every only).
	return cs, nil
}

// bitset is a 64-bit set for cron field matching.
type bitset uint64

func (b bitset) has(v int) bool { return b&(1<<uint(v)) != 0 }
func (b *bitset) set(v int)     { *b |= 1 << uint(v) }

// CronSchedule implements 5-field cron matching.
type CronSchedule struct {
	Minute bitset
	Hour   bitset
	Dom    bitset
	Month  bitset
	Dow    bitset
}

// Next returns the next fire time strictly after 'after'.
func (cs *CronSchedule) Next(after time.Time) time.Time {
	// Start from the next minute.
	t := after.Truncate(time.Minute).Add(time.Minute)

	// Search up to 4 years ahead to handle all edge cases.
	limit := t.Add(4 * 365 * 24 * time.Hour)

	for t.Before(limit) {
		// Check month.
		if !cs.Month.has(int(t.Month())) {
			// Advance to start of next month.
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
			continue
		}

		// Check day-of-month and day-of-week (both must match).
		if !cs.Dom.has(t.Day()) || !cs.Dow.has(int(t.Weekday())) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
			continue
		}

		// Check hour.
		if !cs.Hour.has(t.Hour()) {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, t.Location())
			continue
		}

		// Check minute.
		if !cs.Minute.has(t.Minute()) {
			t = t.Add(time.Minute)
			continue
		}

		return t
	}

	// Should not happen for valid expressions within 4 years.
	return time.Time{}
}

// IntervalSchedule fires at fixed intervals.
type IntervalSchedule struct {
	Interval time.Duration
}

// Next returns the next fire time strictly after 'after', aligned to the interval.
func (is *IntervalSchedule) Next(after time.Time) time.Time {
	return after.Truncate(time.Minute).Add(is.Interval)
}

// parseField parses a single cron field into a bitset.
// Supports: *, values, ranges (a-b), steps (*/n, a-b/n), and comma-separated lists.
func parseField(field string, min, max int) (bitset, error) {
	var bs bitset

	parts := strings.Split(field, ",")
	for _, part := range parts {
		partBs, err := parseFieldPart(part, min, max)
		if err != nil {
			return 0, err
		}
		bs |= partBs
	}

	if bs == 0 {
		return 0, fmt.Errorf("field %q produced empty set", field)
	}

	return bs, nil
}

func parseFieldPart(part string, min, max int) (bitset, error) {
	var bs bitset

	// Check for step: "X/N"
	rangeExpr := part
	step := 1
	if idx := strings.Index(part, "/"); idx != -1 {
		var err error
		step, err = strconv.Atoi(part[idx+1:])
		if err != nil || step <= 0 {
			return 0, fmt.Errorf("invalid step in %q", part)
		}
		rangeExpr = part[:idx]
	}

	// Determine the range.
	var lo, hi int
	switch {
	case rangeExpr == "*":
		lo, hi = min, max
	case strings.Contains(rangeExpr, "-"):
		rangeParts := strings.SplitN(rangeExpr, "-", 2)
		var err error
		lo, err = strconv.Atoi(rangeParts[0])
		if err != nil {
			return 0, fmt.Errorf("invalid range start in %q", part)
		}
		hi, err = strconv.Atoi(rangeParts[1])
		if err != nil {
			return 0, fmt.Errorf("invalid range end in %q", part)
		}
	default:
		val, err := strconv.Atoi(rangeExpr)
		if err != nil {
			return 0, fmt.Errorf("invalid value %q", part)
		}
		if step > 1 {
			// Single value with step, e.g. "5/15" means starting at 5, every 15
			lo, hi = val, max
		} else {
			if val < min || val > max {
				return 0, fmt.Errorf("value %d out of range [%d, %d]", val, min, max)
			}
			bs.set(val)
			return bs, nil
		}
	}

	if lo < min || hi > max || lo > hi {
		return 0, fmt.Errorf("range %d-%d out of bounds [%d, %d]", lo, hi, min, max)
	}

	for v := lo; v <= hi; v += step {
		bs.set(v)
	}

	return bs, nil
}
