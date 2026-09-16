package optimizer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeLog(t *testing.T, dir string, lines []map[string]any) string {
	t.Helper()
	path := filepath.Join(dir, "usage.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	enc := json.NewEncoder(f)
	for _, l := range lines {
		if err := enc.Encode(l); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func line(ago time.Duration, now time.Time, session, client, model string, saved, before int) map[string]any {
	return map[string]any{
		"time":       now.Add(-ago).UTC().Format(time.RFC3339),
		"session_id": session, "client": client,
		"usage":       map[string]any{"model": model, "input_tokens": 100, "output_tokens": 10, "cache_read_input_tokens": 500, "cache_creation_input_tokens": 400},
		"compression": map[string]any{"saved_tokens": saved, "tokens_before": before, "tokens_after": before - saved},
	}
}

func TestAggregateUsageLog(t *testing.T) {
	// Anchor to a fixed midday so the "N hours ago" rows stay within the same
	// calendar day as `now` regardless of the wall-clock time the test runs at
	// (a plain time.Now() flakes when the suite runs just after midnight).
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	path := writeLog(t, dir, []map[string]any{
		line(1*time.Hour, now, "sess_a", "claude-code", "claude-opus-4-8", 9000, 14000),      // today
		line(2*time.Hour, now, "sess_a", "claude-code", "claude-opus-4-8", 10000, 14000),     // today, same session
		line(3*24*time.Hour, now, "sess_b", "codex", "claude-sonnet-5", 18000, 28000),        // within 7d
		line(20*24*time.Hour, now, "sess_c", "claude-code", "claude-opus-4-8", 26000, 40000), // within 30d
		line(40*24*time.Hour, now, "sess_d", "codex", "claude-haiku-4-5", 5000, 8000),        // outside 30d (dropped from windows, kept as session)
	})

	rep, err := AggregateUsageLog(path, NewPricing(nil), now, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Records != 5 {
		t.Fatalf("records = %d, want 5", rep.Records)
	}
	// Today = two sess_a rows (file order: 9000 then 10000), saved 19000, billed
	// cache-write = 2×400 = 800. Each row splits its saved between input:write by
	// input(100):cw(400) = 1:4 → 20% input, 80% write. The 2nd row also credits
	// the 1st row's 9000 saved as an avoided compounding cache-read.
	//   avoided input  = .2×19000 = 3800
	//   avoided write  = .8×19000 = 15200
	//   avoided read   = 9000 (only the 2nd row sees a prior cumulative)
	if rep.Today.SavedTokens != 19000 || rep.Today.CacheWriteTokens != 800 {
		t.Errorf("today = %+v", rep.Today)
	}
	if rep.Today.AvoidedInputTokens != 3800 || rep.Today.AvoidedCacheWriteTokens != 15200 || rep.Today.AvoidedCacheReadTokens != 9000 {
		t.Errorf("today breakdown = %+v", rep.Today)
	}
	// Today dollars (opus $5/M: input 1×, write 1.25×=6.25, read 0.1×=0.5):
	//   3800×5 + 15200×6.25 + 9000×0.5 = 19000 + 95000 + 4500 = 118500 /1e6 = $0.1185
	if got := rep.Today.Dollars; got < 0.11845 || got > 0.11855 {
		t.Errorf("today dollars = %f, want ~0.1185", got)
	}
	// 30d excludes the 40-day-old row: saved 9000+10000+18000+26000 = 63000.
	if rep.Last30Days.SavedTokens != 63000 {
		t.Errorf("30d saved = %d, want 63000", rep.Last30Days.SavedTokens)
	}
	// Sessions: all 4 distinct ids present (sess_d kept even though outside 30d).
	if len(rep.Sessions) != 4 {
		t.Fatalf("sessions = %d, want 4", len(rep.Sessions))
	}
	// Most-recent first → sess_a leads and aggregates its two requests.
	if rep.Sessions[0].SessionID != "sess_a" || rep.Sessions[0].Requests != 2 || rep.Sessions[0].SavedTokens != 19000 {
		t.Errorf("sess_a rollup wrong: %+v", rep.Sessions[0])
	}
	// per-client (30d window): claude-code 2 sessions' rows = 3 calls, codex = 1 call (sess_b; sess_d dropped).
	if rep.PerClient["claude-code"].Calls != 3 {
		t.Errorf("claude-code calls = %d, want 3", rep.PerClient["claude-code"].Calls)
	}
	if rep.PerClient["codex"].Calls != 1 {
		t.Errorf("codex calls = %d, want 1", rep.PerClient["codex"].Calls)
	}
}

func TestAggregateUsageLog_MissingFile(t *testing.T) {
	rep, err := AggregateUsageLog(filepath.Join(t.TempDir(), "nope.jsonl"), NewPricing(nil), time.Now(), 0)
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if rep.Records != 0 || len(rep.Sessions) != 0 {
		t.Errorf("expected empty report, got %+v", rep)
	}
}
