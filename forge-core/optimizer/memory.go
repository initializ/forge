package optimizer

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Local memory path for coding agents (the "wire adapter", memory-capability-v2
// §12.8). The optimizer sits on the wire, so it forms memory ambiently by
// distilling episodes from the transcript — it never needs the agent to call a
// memory API. This file is the LOCAL provider (OSS default): a durable,
// file-backed store under ~/.forge that `forge optimizer memory` and the
// forge-ui dashboard read. When a control-plane URL is later configured, a
// remote provider becomes authoritative and this local store becomes a
// write-through cache — but the record shape here is the shared content schema.
//
// Coding profile: records are scoped to a repo (`about: resource:repo`),
// episodic + procedural (semantic off — the codebase is the semantic store),
// and carry code_state so a memory can decay against a moving codebase.

// Outcome classifications for an episode.
const (
	OutcomeSuccess   = "success"
	OutcomeFailure   = "failure"
	OutcomeAbandoned = "abandoned"
	OutcomeUnknown   = "unknown"
)

// Kind discriminates memory records, matching memory-capability-v2's canonical
// schema (semantic | episodic | procedural). The wire adapter forms episodic
// records directly; procedural records are consolidated from clusters of
// episodes (see memory_consolidate.go). Semantic is deliberately unused for
// coding — the codebase is the semantic store.
const (
	KindEpisodic   = "episodic"
	KindProcedural = "procedural"
)

// CodeState grounds a record in the repo state it was formed against, so recall
// can down-weight or re-validate a memory when the code has moved (§12.8).
type CodeState struct {
	Commit string `json:"commit,omitempty"`
}

// Episode is one completed unit of work, LLM-distilled from the transcript.
//
// The content-axis fields (kind, about, source_kind, entities, confidence,
// task_signature, outcome, timestamps) mirror memory-capability-v2's canonical
// record so the local store is a clean subset of the platform envelope. The
// identity/provenance axis (actor_workload_id, principal_sub, …) is added only
// when a record crosses to the control plane (§12.8, Rule P0) — never locally.
type Episode struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`  // episodic (canonical discriminator)
	Repo  string `json:"repo"`  // convenience; canonical scope is About
	About string `json:"about"` // resource:repo:<repo>
	// Entities are the linking keys extracted at write time (files touched).
	// Records sharing an entity are related — the coding analogue of the design's
	// entity graph.
	Entities      []string  `json:"entities,omitempty"`
	SessionID     string    `json:"session_id,omitempty"`
	Client        string    `json:"client,omitempty"`
	Model         string    `json:"model,omitempty"`
	TaskSignature string    `json:"task_signature"`
	Summary       string    `json:"summary"`
	Actions       []string  `json:"actions,omitempty"`
	Outcome       string    `json:"outcome"`
	Lesson        string    `json:"lesson,omitempty"`
	Files         []string  `json:"files,omitempty"`
	Errors        []string  `json:"errors,omitempty"`
	CodeState     CodeState `json:"code_state,omitempty"`
	// Provenance (content axis). The governed identity envelope (P0) is added
	// only when a record crosses to the control plane.
	SourceKind string  `json:"source_kind"` // agent_inference for wire-distilled
	Binding    string  `json:"binding"`     // "inferred" — task boundary/outcome inferred (§12.8 DS8)
	Confidence float64 `json:"confidence"`
	CreatedAt  string  `json:"created_at"`
	// EpisodeIDs is the evidence for a PROCEDURAL record — the episodes it was
	// consolidated from (memory-capability-v2 evidence.episode_ids). Empty for
	// episodic records. For procedural records: Summary is the procedure
	// description, Actions are the steps, Errors are the pitfalls.
	EpisodeIDs []string `json:"episode_ids,omitempty"`

	// Derived, populated on read for display — NEVER persisted (zero at write, so
	// omitempty omits them). RecallCount is the number of distinct sessions this
	// episode was injected into; LastRecalled is the most recent such time;
	// EffectiveConfidence is the base Confidence adjusted by feedback (see
	// memory_feedback.go).
	RecallCount         int     `json:"recall_count,omitempty"`
	LastRecalled        string  `json:"last_recalled,omitempty"`
	EffectiveConfidence float64 `json:"effective_confidence,omitempty"`
}

// RecallStat aggregates how often an episode has been injected into sessions.
type RecallStat struct {
	Count        int    `json:"count"`
	LastRecalled string `json:"last_recalled"`
}

// FeedbackStat aggregates the positive/negative evidence weight accumulated for
// one memory (see memory_feedback.go). Pos/Neg feed the Beta-style effective
// confidence.
type FeedbackStat struct {
	Pos  float64 `json:"pos"`
	Neg  float64 `json:"neg"`
	Last string  `json:"last"`
}

// feedbackEvent is one confidence-moving signal for a memory.
type feedbackEvent struct {
	MemoryID string  `json:"memory_id"`
	Signal   string  `json:"signal"` // up | down | corroborate | recall_success | recall_failure
	Weight   float64 `json:"weight"` // precomputed magnitude (>=0), applied to Pos or Neg
	Positive bool    `json:"positive"`
	At       string  `json:"at"`
}

// recallEvent is one "episode E was injected into session S at T" record,
// appended to a sibling log so per-episode usage can be shown.
type recallEvent struct {
	EpisodeID string `json:"episode_id"`
	SessionID string `json:"session_id"`
	At        string `json:"at"`
}

// MemoryStore is the local memory provider seam. A control-plane driver will
// implement the same interface behind a URL switch.
type MemoryStore interface {
	WriteEpisode(e Episode) error
	ListEpisodes(repo string, limit int) ([]Episode, error) // repo "" = all repos
	DeleteEpisode(id string) (bool, error)                  // false if not found
	// RecordRecall notes that these episodes were injected into a session's
	// context (once per session, since recall is frozen per session).
	RecordRecall(sessionID string, episodeIDs []string) error
	// RecallCounts returns per-episode recall aggregates (distinct sessions).
	RecallCounts() (map[string]RecallStat, error)
	// RecalledInto returns the distinct memory ids that were injected into a
	// session (used for post-recall outcome attribution).
	RecalledInto(sessionID string) ([]string, error)
	// RecordFeedback appends a confidence-moving signal for a memory.
	RecordFeedback(memoryID, signal string, weight float64, positive bool) error
	// FeedbackAgg returns per-memory accumulated positive/negative evidence.
	FeedbackAgg() (map[string]FeedbackStat, error)
	Close() error
}

// fileMemoryStore appends episodes as NDJSON under a single file. Simple and
// durable; recall/list read-and-filter (fine at OSS scale — the mongo-scan tier
// of the memory plan). Concurrent writes are serialized.
type fileMemoryStore struct {
	path string
	mu   sync.Mutex
}

// NewFileMemoryStore opens (creating parents) a file-backed local memory store.
func NewFileMemoryStore(path string) (MemoryStore, error) {
	if path == "" {
		return nil, fmt.Errorf("optimizer: memory store path is required")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("optimizer: creating memory dir: %w", err)
		}
	}
	return &fileMemoryStore{path: path}, nil
}

// WriteEpisode appends one episode.
func (s *fileMemoryStore) WriteEpisode(e Episode) error {
	if e.CreatedAt == "" {
		e.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.Write(append(line, '\n'))
	return err
}

// ListEpisodes returns episodes (optionally filtered by repo), most-recent
// first, capped to limit (0 = default 200).
func (s *fileMemoryStore) ListEpisodes(repo string, limit int) ([]Episode, error) {
	if limit <= 0 {
		limit = 200
	}
	s.mu.Lock()
	f, err := os.Open(s.path)
	s.mu.Unlock()
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var out []Episode
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Episode
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		if repo != "" && e.Repo != repo {
			continue
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// DeleteEpisode removes the episode with the given id by rewriting the log
// without it (atomic temp-file + rename). Returns whether a record was removed.
func (s *fileMemoryStore) DeleteEpisode(id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	var kept [][]byte
	found := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Episode
		if json.Unmarshal(line, &e) == nil && e.ID == id {
			found = true
			continue // drop it
		}
		kept = append(kept, append([]byte(nil), line...))
	}
	_ = f.Close()
	if err := sc.Err(); err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}

	tmp := s.path + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return false, err
	}
	for _, line := range kept {
		if _, err := out.Write(append(line, '\n')); err != nil {
			_ = out.Close()
			_ = os.Remove(tmp)
			return false, err
		}
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	return true, nil
}

// recallsPath is the sibling log of recall events.
func (s *fileMemoryStore) recallsPath() string { return s.path + ".recalls.jsonl" }

// RecordRecall appends one event per injected episode.
func (s *fileMemoryStore) RecordRecall(sessionID string, episodeIDs []string) error {
	if len(episodeIDs) == 0 {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.recallsPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	for _, id := range episodeIDs {
		if id == "" {
			continue
		}
		line, err := json.Marshal(recallEvent{EpisodeID: id, SessionID: sessionID, At: now})
		if err != nil {
			return err
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			return err
		}
	}
	return nil
}

// RecallCounts aggregates recall events into per-episode {count, last_recalled},
// where count is the number of DISTINCT sessions an episode was injected into.
func (s *fileMemoryStore) RecallCounts() (map[string]RecallStat, error) {
	s.mu.Lock()
	f, err := os.Open(s.recallsPath())
	s.mu.Unlock()
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]RecallStat{}, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	sessionsPerEp := map[string]map[string]struct{}{} // episode -> set of sessions
	last := map[string]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev recallEvent
		if json.Unmarshal(line, &ev) != nil || ev.EpisodeID == "" {
			continue
		}
		set := sessionsPerEp[ev.EpisodeID]
		if set == nil {
			set = map[string]struct{}{}
			sessionsPerEp[ev.EpisodeID] = set
		}
		set[ev.SessionID] = struct{}{}
		if ev.At > last[ev.EpisodeID] {
			last[ev.EpisodeID] = ev.At
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	out := make(map[string]RecallStat, len(sessionsPerEp))
	for id, set := range sessionsPerEp {
		out[id] = RecallStat{Count: len(set), LastRecalled: last[id]}
	}
	return out, nil
}

// RecalledInto returns the distinct memory ids injected into the given session.
func (s *fileMemoryStore) RecalledInto(sessionID string) ([]string, error) {
	s.mu.Lock()
	f, err := os.Open(s.recallsPath())
	s.mu.Unlock()
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	seen := map[string]struct{}{}
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
	for sc.Scan() {
		var ev recallEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.SessionID != sessionID {
			continue
		}
		if _, ok := seen[ev.EpisodeID]; ok {
			continue
		}
		seen[ev.EpisodeID] = struct{}{}
		out = append(out, ev.EpisodeID)
	}
	return out, sc.Err()
}

// feedbackPath is the sibling log of confidence-moving events.
func (s *fileMemoryStore) feedbackPath() string { return s.path + ".feedback.jsonl" }

// RecordFeedback appends one confidence signal.
func (s *fileMemoryStore) RecordFeedback(memoryID, signal string, weight float64, positive bool) error {
	if memoryID == "" || weight <= 0 {
		return nil
	}
	line, err := json.Marshal(feedbackEvent{
		MemoryID: memoryID, Signal: signal, Weight: weight, Positive: positive,
		At: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.feedbackPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.Write(append(line, '\n'))
	return err
}

// FeedbackAgg sums positive/negative evidence per memory.
func (s *fileMemoryStore) FeedbackAgg() (map[string]FeedbackStat, error) {
	s.mu.Lock()
	f, err := os.Open(s.feedbackPath())
	s.mu.Unlock()
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]FeedbackStat{}, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	out := map[string]FeedbackStat{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
	for sc.Scan() {
		var ev feedbackEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.MemoryID == "" {
			continue
		}
		st := out[ev.MemoryID]
		if ev.Positive {
			st.Pos += ev.Weight
		} else {
			st.Neg += ev.Weight
		}
		if ev.At > st.Last {
			st.Last = ev.At
		}
		out[ev.MemoryID] = st
	}
	return out, sc.Err()
}

// Close is a no-op for the file store (each write opens/closes).
func (s *fileMemoryStore) Close() error { return nil }
