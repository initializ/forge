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
	// requests — the freshly-cached bytes each turn adds. The ratio
	// SavedTokens/CacheWriteTokens = "% of the newly-cached token volume
	// compression removed"; it drives the progress bar.
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	// Cost-avoided breakdown: without compression the saved content would have
	// been billed across three tiers — uncached INPUT (1×) and CACHE-WRITE (1.25×)
	// on the turn it first appears, plus a compounding CACHE-READ (0.1×) for every
	// later turn in the session that no longer re-reads it. AvoidedCacheReadTokens
	// therefore far exceeds SavedTokens (each saved chunk is re-read many turns).
	AvoidedInputTokens      int64 `json:"avoided_input_tokens"`
	AvoidedCacheWriteTokens int64 `json:"avoided_cache_write_tokens"`
	AvoidedCacheReadTokens  int64 `json:"avoided_cache_read_tokens"`
	// Per-tier avoided dollars (so the UI can show the $ composition), their sum
	// Dollars, and SpentUSD — what these requests actually cost — so cost-avoided
	// can be anchored against real spend (the "avoided vs spent" ratio).
	AvoidedInputUSD      float64 `json:"avoided_input_usd"`
	AvoidedCacheWriteUSD float64 `json:"avoided_cache_write_usd"`
	AvoidedCacheReadUSD  float64 `json:"avoided_cache_read_usd"`
	Dollars              float64 `json:"cost_avoided_usd"`
	SpentUSD             float64 `json:"spent_usd"`
}

// recordCredit is one request's contribution to a window.
type recordCredit struct {
	saved, billedCacheWrite                     int64
	avInput, avWrite, avRead                    int64
	avInputUSD, avWriteUSD, avReadUSD, spentUSD float64
}

func (w *WindowTotals) add(c recordCredit) {
	w.SavedTokens += c.saved
	w.CacheWriteTokens += c.billedCacheWrite
	w.AvoidedInputTokens += c.avInput
	w.AvoidedCacheWriteTokens += c.avWrite
	w.AvoidedCacheReadTokens += c.avRead
	w.AvoidedInputUSD += c.avInputUSD
	w.AvoidedCacheWriteUSD += c.avWriteUSD
	w.AvoidedCacheReadUSD += c.avReadUSD
	w.Dollars += c.avInputUSD + c.avWriteUSD + c.avReadUSD
	w.SpentUSD += c.spentUSD
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
	SpentUSD        float64 `json:"spent_usd"`
	FirstSeen       string  `json:"first_seen"`
	LastSeen        string  `json:"last_seen"`
}

// ReportTotals is the all-time, durable header tally over every record in the
// log. It is the single source for the dashboard's header tiles, so the token
// counts and the dollar figures share one scope (no live-vs-durable mismatch).
type ReportTotals struct {
	Requests         int64   `json:"requests"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_input_tokens"`
	CacheWriteTokens int64   `json:"cache_write_input_tokens"`
	SavedTokens      int64   `json:"saved_tokens"`
	AvoidedUSD       float64 `json:"cost_avoided_usd"`
	SpentUSD         float64 `json:"spent_usd"`
}

// UsageReport is the full durable rollup returned by AggregateUsageLog.
type UsageReport struct {
	Totals     ReportTotals             `json:"totals"`
	AllTime    WindowTotals             `json:"all_time"`
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
	// Per-session running total of tokens compressed away so far. A chunk removed
	// on an earlier turn stays absent from every later turn's cached prefix, so it
	// avoids a cache-READ on each of those turns — this cumulative drives the
	// compounding read credit. Records stream in append (chronological) order, so
	// this is correct even with sessions interleaved.
	cumSaved := map[string]int64{}

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
		billedCacheWrite := int64(rec.Usage.CacheCreationInputTokens)

		sid := rec.SessionID
		if sid == "" {
			sid = "unknown"
		}

		// Three-tier cost-avoided attribution:
		//  - CACHE-READ (compounding): every token compressed away on an EARLIER
		//    turn is absent from this turn's cached prefix, so it avoids a cache
		//    read now. cumSaved[sid] (prior turns) is that count.
		//  - This turn's own saved tokens are "born" here: split them between
		//    uncached INPUT and CACHE-WRITE by this request's input:cache_creation
		//    ratio (the tier mix new content actually lands in). No new-content
		//    signal (both zero) → treat as cache-write, since content must be
		//    cached to persist into later turns.
		avRead := cumSaved[sid]
		it := int64(rec.Usage.InputTokens)
		cw := billedCacheWrite
		cr := int64(rec.Usage.CacheReadInputTokens)
		out := int64(rec.Usage.OutputTokens)
		var avInput, avWrite int64
		if base := it + cw; base > 0 {
			avInput = saved * it / base
			avWrite = saved - avInput
		} else {
			avWrite = saved
		}
		dIn, dWr, dRd := pricing.AvoidedTiers(rec.Usage.Model, avInput, avWrite, avRead)
		spent := pricing.SpendUSD(rec.Usage.Model, it, cw, cr, out)
		cumSaved[sid] += saved

		credit := recordCredit{
			saved: saved, billedCacheWrite: billedCacheWrite,
			avInput: avInput, avWrite: avWrite, avRead: avRead,
			avInputUSD: dIn, avWriteUSD: dWr, avReadUSD: dRd, spentUSD: spent,
		}
		dollars := dIn + dWr + dRd

		// All-time header totals (every record, one shared scope for the header).
		rep.Totals.Requests++
		rep.Totals.InputTokens += it
		rep.Totals.OutputTokens += out
		rep.Totals.CacheReadTokens += cr
		rep.Totals.CacheWriteTokens += cw
		rep.Totals.SavedTokens += saved
		rep.Totals.AvoidedUSD += dollars
		rep.Totals.SpentUSD += spent
		rep.AllTime.add(credit)

		// Windowed dollar summaries (30-day horizon).
		if !rec.Time.Before(win30) {
			rep.Last30Days.add(credit)
			if !rec.Time.Before(win7) {
				rep.Last7Days.add(credit)
			}
			if !rec.Time.Before(startToday) {
				rep.Today.add(credit)
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
		s.SpentUSD += spent
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
