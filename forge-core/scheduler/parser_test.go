package scheduler

import (
	"testing"
	"time"
)

func TestParse_Valid(t *testing.T) {
	tests := []struct {
		expr string
	}{
		{"* * * * *"},
		{"0 * * * *"},
		{"*/15 * * * *"},
		{"0 0 * * *"},
		{"0 0 1 * *"},
		{"0 0 * * 0"},
		{"30 8 * * 1-5"},
		{"0 9,17 * * *"},
		{"0 0 1,15 * *"},
		{"5/15 * * * *"},
		{"0 0 * 1-6 *"},
		{"@hourly"},
		{"@daily"},
		{"@weekly"},
		{"@monthly"},
		{"@every 5m"},
		{"@every 1h"},
		{"@every 1h30m"},
	}

	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			_, err := Parse(tt.expr)
			if err != nil {
				t.Fatalf("Parse(%q) returned error: %v", tt.expr, err)
			}
		})
	}
}

func TestParse_Invalid(t *testing.T) {
	tests := []struct {
		expr string
	}{
		{""},
		{"* * *"},
		{"* * * * * *"},
		{"60 * * * *"},
		{"* 24 * * *"},
		{"* * 0 * *"},
		{"* * 32 * *"},
		{"* * * 0 *"},
		{"* * * 13 *"},
		{"* * * * 7"},
		{"@every 30s"},
		{"@every invalid"},
		{"abc * * * *"},
		{"1-0 * * * *"},
	}

	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			_, err := Parse(tt.expr)
			if err == nil {
				t.Fatalf("Parse(%q) expected error, got nil", tt.expr)
			}
		})
	}
}

func TestCronSchedule_Next(t *testing.T) {
	loc := time.UTC

	tests := []struct {
		name     string
		expr     string
		after    time.Time
		expected time.Time
	}{
		{
			name:     "every minute from top of hour",
			expr:     "* * * * *",
			after:    time.Date(2026, 1, 1, 0, 0, 0, 0, loc),
			expected: time.Date(2026, 1, 1, 0, 1, 0, 0, loc),
		},
		{
			name:     "hourly",
			expr:     "0 * * * *",
			after:    time.Date(2026, 1, 1, 0, 30, 0, 0, loc),
			expected: time.Date(2026, 1, 1, 1, 0, 0, 0, loc),
		},
		{
			name:     "daily at midnight",
			expr:     "0 0 * * *",
			after:    time.Date(2026, 1, 1, 12, 0, 0, 0, loc),
			expected: time.Date(2026, 1, 2, 0, 0, 0, 0, loc),
		},
		{
			name:     "every 15 minutes",
			expr:     "*/15 * * * *",
			after:    time.Date(2026, 1, 1, 0, 10, 0, 0, loc),
			expected: time.Date(2026, 1, 1, 0, 15, 0, 0, loc),
		},
		{
			name:     "weekdays at 8:30",
			expr:     "30 8 * * 1-5",
			after:    time.Date(2026, 1, 3, 9, 0, 0, 0, loc),  // Saturday
			expected: time.Date(2026, 1, 5, 8, 30, 0, 0, loc), // Monday
		},
		{
			name:     "month boundary",
			expr:     "0 0 1 * *",
			after:    time.Date(2026, 1, 15, 0, 0, 0, 0, loc),
			expected: time.Date(2026, 2, 1, 0, 0, 0, 0, loc),
		},
		{
			name:     "year boundary",
			expr:     "0 0 1 1 *",
			after:    time.Date(2026, 6, 1, 0, 0, 0, 0, loc),
			expected: time.Date(2027, 1, 1, 0, 0, 0, 0, loc),
		},
		{
			name:     "specific months",
			expr:     "0 9 * 3,6,9,12 *",
			after:    time.Date(2026, 4, 1, 0, 0, 0, 0, loc),
			expected: time.Date(2026, 6, 1, 9, 0, 0, 0, loc),
		},
		{
			name:     "9 and 17 hours",
			expr:     "0 9,17 * * *",
			after:    time.Date(2026, 1, 1, 10, 0, 0, 0, loc),
			expected: time.Date(2026, 1, 1, 17, 0, 0, 0, loc),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sched, err := Parse(tt.expr)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tt.expr, err)
			}
			got := sched.Next(tt.after)
			if !got.Equal(tt.expected) {
				t.Errorf("Next(%v) = %v, want %v", tt.after, got, tt.expected)
			}
		})
	}
}

func TestIntervalSchedule_Next(t *testing.T) {
	after := time.Date(2026, 1, 1, 0, 0, 30, 0, time.UTC) // 30 seconds past
	sched, err := Parse("@every 5m")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	got := sched.Next(after)
	// Truncates to minute (00:00) then adds 5m → 00:05
	expected := time.Date(2026, 1, 1, 0, 5, 0, 0, time.UTC)
	if !got.Equal(expected) {
		t.Errorf("Next(%v) = %v, want %v", after, got, expected)
	}
}

func TestParse_Aliases(t *testing.T) {
	// Verify aliases produce correct next times.
	loc := time.UTC
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, loc)

	tests := []struct {
		alias    string
		expected time.Time
	}{
		{"@hourly", time.Date(2026, 1, 1, 1, 0, 0, 0, loc)},
		{"@daily", time.Date(2026, 1, 2, 0, 0, 0, 0, loc)},
		{"@monthly", time.Date(2026, 2, 1, 0, 0, 0, 0, loc)},
	}

	for _, tt := range tests {
		t.Run(tt.alias, func(t *testing.T) {
			sched, err := Parse(tt.alias)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tt.alias, err)
			}
			got := sched.Next(after)
			if !got.Equal(tt.expected) {
				t.Errorf("Next(%v) = %v, want %v", after, got, tt.expected)
			}
		})
	}
}
