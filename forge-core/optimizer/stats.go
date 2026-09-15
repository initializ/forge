package optimizer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Totals is a running token/request tally.
type Totals struct {
	Requests                 int64 `json:"requests"`
	Errors                   int64 `json:"errors"`
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	// CompressionSavedTokens is the cumulative tokenizer-estimated reduction the
	// optimizer's compression applied to outbound bodies.
	CompressionSavedTokens int64 `json:"compression_saved_tokens"`
	CompressedBlocks       int64 `json:"compressed_blocks"`
	// Expansions / ExpansionMisses count context_expand retrievals — the cost
	// side to net against compression savings.
	Expansions      int64 `json:"expansions"`
	ExpansionMisses int64 `json:"expansion_misses"`
}

func (t *Totals) add(r Report) {
	t.Requests++
	if r.Error != "" {
		t.Errors++
	}
	t.InputTokens += int64(r.Usage.InputTokens)
	t.OutputTokens += int64(r.Usage.OutputTokens)
	t.CacheReadInputTokens += int64(r.Usage.CacheReadInputTokens)
	t.CacheCreationInputTokens += int64(r.Usage.CacheCreationInputTokens)
	t.CompressionSavedTokens += int64(r.Compression.SavedTokens)
	t.CompressedBlocks += int64(r.Compression.Blocks)
}

// StatsReporter keeps in-memory running totals plus the most recent requests,
// and serves them as JSON at /stats. It is always wired into the server so
// local usage is visible (curl http://127.0.0.1:8787/stats) without any control
// plane. Bounded recent-buffer keeps memory flat over a long session.
type StatsReporter struct {
	startedAt time.Time

	mu          sync.Mutex
	totals      Totals
	perModel    map[string]*Totals
	perSession  map[string]*sessionTotals
	sessOrder   []string
	maxSessions int
	recent      []Report
	maxRecent   int
}

// sessionTotals is one session's tally plus first/last-seen timestamps.
type sessionTotals struct {
	Totals
	firstSeen time.Time
	lastSeen  time.Time
}

// maxTrackedSessions bounds the per-session map so a long-lived shared proxy
// can't grow unbounded; the oldest-touched session is evicted past the cap.
const maxTrackedSessions = 256

// NewStatsReporter constructs a StatsReporter retaining the last maxRecent
// reports (default 50 if <= 0).
func NewStatsReporter(maxRecent int) *StatsReporter {
	if maxRecent <= 0 {
		maxRecent = 50
	}
	return &StatsReporter{
		startedAt:   time.Now(),
		perModel:    map[string]*Totals{},
		perSession:  map[string]*sessionTotals{},
		maxSessions: maxTrackedSessions,
		maxRecent:   maxRecent,
	}
}

// Report implements Reporter.
func (s *StatsReporter) Report(_ context.Context, r Report) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.totals.add(r)
	model := r.Usage.Model
	if model == "" {
		model = "unknown"
	}
	pm := s.perModel[model]
	if pm == nil {
		pm = &Totals{}
		s.perModel[model] = pm
	}
	pm.add(r)

	session := r.SessionID
	if session == "" {
		session = "unknown"
	}
	ps := s.perSession[session]
	if ps == nil {
		s.evictSessionIfFull()
		ps = &sessionTotals{firstSeen: time.Now()}
		s.perSession[session] = ps
		s.sessOrder = append(s.sessOrder, session)
	}
	ps.Totals.add(r)
	ps.lastSeen = time.Now()

	s.recent = append(s.recent, r)
	if len(s.recent) > s.maxRecent {
		s.recent = s.recent[len(s.recent)-s.maxRecent:]
	}
}

// evictSessionIfFull drops the oldest-inserted session when at capacity. Caller
// holds s.mu.
func (s *StatsReporter) evictSessionIfFull() {
	if len(s.perSession) < s.maxSessions {
		return
	}
	if len(s.sessOrder) == 0 {
		return
	}
	oldest := s.sessOrder[0]
	s.sessOrder = s.sessOrder[1:]
	delete(s.perSession, oldest)
}

// RecordExpansion bumps the expansion counters — called by the context_expand
// MCP server on each retrieval. Kept separate from Report because expansions
// arrive on the MCP endpoint, not the proxy path.
func (s *StatsReporter) RecordExpansion(hit bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.totals.Expansions++
	if !hit {
		s.totals.ExpansionMisses++
	}
}

// SessionStats is one session's totals plus when it was first/last seen.
type SessionStats struct {
	Totals
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`
}

// Snapshot is the JSON shape served at /stats.
type Snapshot struct {
	UptimeSeconds int64                   `json:"uptime_seconds"`
	Totals        Totals                  `json:"totals"`
	PerModel      map[string]Totals       `json:"per_model"`
	Sessions      map[string]SessionStats `json:"sessions"`
	Recent        []Report                `json:"recent"`
}

func (s *StatsReporter) snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	pm := make(map[string]Totals, len(s.perModel))
	for k, v := range s.perModel {
		pm[k] = *v
	}
	sessions := make(map[string]SessionStats, len(s.perSession))
	for k, v := range s.perSession {
		sessions[k] = SessionStats{
			Totals:    v.Totals,
			FirstSeen: v.firstSeen.UTC().Format(time.RFC3339),
			LastSeen:  v.lastSeen.UTC().Format(time.RFC3339),
		}
	}
	recent := make([]Report, len(s.recent))
	copy(recent, s.recent)
	return Snapshot{
		UptimeSeconds: int64(time.Since(s.startedAt).Seconds()),
		Totals:        s.totals,
		PerModel:      pm,
		Sessions:      sessions,
		Recent:        recent,
	}
}

// ServeHTTP renders the snapshot as pretty JSON. With ?session=<id> it returns
// just that session's totals plus its recent requests.
func (s *StatsReporter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	snap := s.snapshot()
	if id := r.URL.Query().Get("session"); id != "" {
		recent := make([]Report, 0, len(snap.Recent))
		for _, rep := range snap.Recent {
			if rep.SessionID == id {
				recent = append(recent, rep)
			}
		}
		_ = enc.Encode(struct {
			Session string       `json:"session"`
			Stats   SessionStats `json:"stats"`
			Recent  []Report     `json:"recent"`
		}{Session: id, Stats: snap.Sessions[id], Recent: recent})
		return
	}
	_ = enc.Encode(snap)
}

// FileReporter appends each report as one NDJSON line to a local file, so usage
// is durable and greppable (tail -f .forge/optimizer-usage.jsonl) before any
// control-plane wiring exists.
type FileReporter struct {
	mu sync.Mutex
	f  *os.File
}

// NewFileReporter opens (creating parents) path for append.
func NewFileReporter(path string) (*FileReporter, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("optimizer: creating usage-log dir: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("optimizer: opening usage log: %w", err)
	}
	return &FileReporter{f: f}, nil
}

// Report implements Reporter.
func (r *FileReporter) Report(_ context.Context, rep Report) {
	line, err := json.Marshal(struct {
		Time string `json:"time"`
		Report
	}{
		Time:   time.Now().UTC().Format(time.RFC3339),
		Report: rep,
	})
	if err != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, _ = r.f.Write(append(line, '\n'))
}

// Close releases the underlying file.
func (r *FileReporter) Close() error {
	if r.f == nil {
		return nil
	}
	return r.f.Close()
}
