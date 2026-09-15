package optimizer

import (
	"bufio"
	"encoding/json"
	"os"
	"sort"
	"time"
)

// This is the durable, restart-surviving view of usage: the in-memory /stats
// resets when the optimizer process exits, but the NDJSON usage log accumulates
// every request. AggregateUsageLog turns that log into windowed savings, plus
// per-session / per-model / per-client rollups, priced in dollars. Both
// `forge optimizer savings` and the forge-ui dashboard read it, so the numbers
// match everywhere.

// WindowTotals is savings over a time window.
type WindowTotals struct {
	SavedTokens int64 `json:"saved_tokens"`
	// CacheWriteTokens is the billed cache-CREATION (write) volume for these
	// requests — the freshly-cached bytes each turn adds. Compression shrinks the
	// conversation history written to the cache, so the ratio
	// SavedTokens/CacheWriteTokens = "% of the newly-cached token volume
	// compression removed". Cache READS are deliberately excluded: they re-count
	// the whole prefix every turn, which would dilute the ratio ~30×.
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	Dollars          float64 `json:"cost_avoided_usd"`
}

func (w *WindowTotals) add(saved, cacheWrite int64, dollars float64) {
	w.SavedTokens += saved
	w.CacheWriteTokens += cacheWrite
	w.Dollars += dollars
}

// ModelSavings is per-model rollup.
type ModelSavings struct {
	SavedTokens int64   `json:"saved_tokens"`
	Dollars     float64 `json:"cost_avoided_usd"`
}

// ClientSavings is per-client rollup.
type ClientSavings struct {
	Calls       int64 `json:"calls"`
	SavedTokens int64 `json:"saved_tokens"`
}

// SessionSavings is one historical Claude Code session, aggregated from every
// request the log recorded under its session id.
type SessionSavings struct {
	SessionID       string  `json:"session_id"`
	Client          string  `json:"client,omitempty"`
	Model           string  `json:"model,omitempty"`
	Requests        int64   `json:"requests"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	CacheReadTokens int64   `json:"cache_read_input_tokens"`
	SavedTokens     int64   `json:"saved_tokens"`
	Dollars         float64 `json:"cost_avoided_usd"`
	FirstSeen       string  `json:"first_seen"`
	LastSeen        string  `json:"last_seen"`
}

// UsageReport is the full durable rollup returned by AggregateUsageLog.
type UsageReport struct {
	Today      WindowTotals             `json:"today"`
	Last7Days  WindowTotals             `json:"last_7_days"`
	Last30Days WindowTotals             `json:"last_30_days"`
	PerModel   map[string]ModelSavings  `json:"per_model"`
	PerClient  map[string]ClientSavings `json:"per_client"`
	Sessions   []SessionSavings         `json:"sessions"` // most-recent first
	Records    int                      `json:"records"`
}

// usageLogLine is one NDJSON line: FileReporter's timestamp + embedded Report.
type usageLogLine struct {
	Time time.Time `json:"time"`
	Report
}

// AggregateUsageLog reads the NDJSON usage log at path and rolls it up, pricing
// savings with the given pricing table as of `now`. A missing file yields an
// empty report (not an error). maxSessions caps the returned session list to
// the most-recently-seen N (0 = default 500).
func AggregateUsageLog(path string, pricing *Pricing, now time.Time, maxSessions int) (*UsageReport, error) {
	if maxSessions <= 0 {
		maxSessions = 500
	}
	rep := &UsageReport{
		PerModel:  map[string]ModelSavings{},
		PerClient: map[string]ClientSavings{},
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return rep, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	startToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	win7 := now.Add(-7 * 24 * time.Hour)
	win30 := now.Add(-30 * 24 * time.Hour)

	// Per-session accumulator keyed by session id (all-time, so history survives
	// beyond the 30-day window used for the dollar summaries).
	sessAcc := map[string]*SessionSavings{}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec usageLogLine
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		rep.Records++

		saved := int64(rec.Compression.SavedTokens)
		// Denominator = billed cache-WRITE (creation) tokens: the freshly-cached
		// bytes compression shrinks each turn. Cache reads are excluded — they
		// re-count the whole prefix every turn and would dilute the ratio ~30×. $
		// credits the cache-WRITE the dropped content would have incurred.
		cacheWrite := int64(rec.Usage.CacheCreationInputTokens)
		dollars := pricing.CacheWriteCostAvoided(rec.Usage.Model, saved)

		// Windowed dollar summaries (30-day horizon).
		if !rec.Time.Before(win30) {
			rep.Last30Days.add(saved, cacheWrite, dollars)
			if !rec.Time.Before(win7) {
				rep.Last7Days.add(saved, cacheWrite, dollars)
			}
			if !rec.Time.Before(startToday) {
				rep.Today.add(saved, cacheWrite, dollars)
			}

			model := rec.Usage.Model
			if model == "" {
				model = "unknown"
			}
			pm := rep.PerModel[model]
			pm.SavedTokens += saved
			pm.Dollars += dollars
			rep.PerModel[model] = pm

			client := rec.Client
			if client == "" {
				client = "unknown"
			}
			pc := rep.PerClient[client]
			pc.Calls++
			pc.SavedTokens += saved
			rep.PerClient[client] = pc
		}

		// Per-session rollup — all-time (this is the durable session history).
		sid := rec.SessionID
		if sid == "" {
			sid = "unknown"
		}
		s := sessAcc[sid]
		if s == nil {
			s = &SessionSavings{SessionID: sid}
			sessAcc[sid] = s
		}
		s.Requests++
		s.InputTokens += int64(rec.Usage.InputTokens)
		s.OutputTokens += int64(rec.Usage.OutputTokens)
		s.CacheReadTokens += int64(rec.Usage.CacheReadInputTokens)
		s.SavedTokens += saved
		s.Dollars += dollars
		if rec.Client != "" {
			s.Client = rec.Client
		}
		if rec.Usage.Model != "" {
			s.Model = rec.Usage.Model
		}
		ts := rec.Time.UTC().Format(time.RFC3339)
		if s.FirstSeen == "" || ts < s.FirstSeen {
			s.FirstSeen = ts
		}
		if ts > s.LastSeen {
			s.LastSeen = ts
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	sessions := make([]SessionSavings, 0, len(sessAcc))
	for _, s := range sessAcc {
		sessions = append(sessions, *s)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].LastSeen > sessions[j].LastSeen })
	if len(sessions) > maxSessions {
		sessions = sessions[:maxSessions]
	}
	rep.Sessions = sessions
	return rep, nil
}
