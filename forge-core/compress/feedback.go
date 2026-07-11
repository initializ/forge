package compress

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/initializ/ctxzip/crush"
)

// The feedback flywheel: every context_expand hit is a signal that
// compression dropped something a model needed. At expansion time — where the
// retrieved original, tool name, and query are all in hand — candidate
// domain-state tokens are extracted from the retrieved content, filtered
// against what the keep floor already protects, and counted across
// expansions. A token that keeps showing up in retrieved content is a
// keep_patterns candidate; at suggestThreshold distinct expansions it is
// surfaced once via a context_pattern_suggested audit event and a log line,
// and `forge compression suggestions` renders the accumulated file as a
// paste-ready forge.yaml block.
//
// Deliberately in-process rather than audit-log mining: audit events go to
// stderr or export sinks the runtime cannot assume it can read back.

const (
	// SuggestionsFileName sits next to the CCR store under .forge/.
	SuggestionsFileName = "ctxzip-suggestions.json"
	// suggestThreshold is how many distinct expansions a token must appear
	// in before it is surfaced as a suggestion.
	suggestThreshold = 3
	// maxTrackedPatterns bounds the file; lowest-count, oldest entries are
	// evicted first.
	maxTrackedPatterns = 512
	// maxCandidatesPerExpansion caps how many tokens one expansion may
	// contribute, keeping a single giant retrieval from flooding the file.
	maxCandidatesPerExpansion = 20
	// maxEventCandidates caps the candidates carried on each
	// context_expanded audit event (highest-frequency first) — enough for
	// platform-side counting without bloating the audit stream.
	maxEventCandidates = 5
)

// AuditEventPatternSuggested fires once per pattern when it crosses
// suggestThreshold. Fields: pattern, expansions, tools.
const AuditEventPatternSuggested = "context_pattern_suggested"

// PatternStat is one tracked keep-pattern candidate.
type PatternStat struct {
	// Pattern preserves the first-seen casing (what the operator would put
	// in keep_patterns).
	Pattern string `json:"pattern"`
	// Expansions counts DISTINCT expansion events whose retrieved content
	// contained the token — not occurrences within one retrieval.
	Expansions int       `json:"expansions"`
	Tools      []string  `json:"tools,omitempty"`
	Suggested  bool      `json:"suggested"`
	LastSeen   time.Time `json:"last_seen"`
}

// suggestionStore persists pattern candidates across restarts. Single-process
// like the CCR store; writes are atomic (temp + rename).
type suggestionStore struct {
	path string

	mu     sync.Mutex
	loaded bool
	data   map[string]*PatternStat // key: lowercase token
}

func newSuggestionStore(path string) *suggestionStore {
	return &suggestionStore{path: path}
}

// candidateRe matches domain-state token shapes: CamelCase words with at
// least two humps (ImagePullBackOff, DiskPressure) and ALLCAPS identifiers
// (QUOTA_EXCEEDED, OOMKILLED). These are the shapes keep_patterns entries
// take in practice.
var candidateRe = regexp.MustCompile(`\b[A-Z][a-z0-9]+(?:[A-Z][a-z0-9]+)+\b|\b[A-Z][A-Z0-9_]{3,}\b`)

// extractCandidates pulls candidate tokens from retrieved content, most
// frequent first, deduped, filtered against the keep floor and the
// configured keep patterns (both already protect their matches — they cannot
// be why the model expanded).
func extractCandidates(content string, keepPatterns []string) []string {
	freq := map[string]int{}
	casing := map[string]string{}
	for _, tok := range candidateRe.FindAllString(content, -1) {
		lower := strings.ToLower(tok)
		if len(lower) < 4 || len(lower) > 48 {
			continue
		}
		if crush.IsErrorLike(lower) {
			continue // already floor-kept
		}
		already := false
		for _, kp := range keepPatterns {
			if strings.Contains(lower, strings.ToLower(kp)) {
				already = true
				break
			}
		}
		if already {
			continue
		}
		freq[lower]++
		if _, ok := casing[lower]; !ok {
			casing[lower] = tok
		}
	}

	out := make([]string, 0, len(freq))
	for l := range freq {
		out = append(out, l)
	}
	sort.Slice(out, func(a, b int) bool {
		if freq[out[a]] != freq[out[b]] {
			return freq[out[a]] > freq[out[b]]
		}
		return out[a] < out[b]
	})
	if len(out) > maxCandidatesPerExpansion {
		out = out[:maxCandidatesPerExpansion]
	}
	for i, l := range out {
		out[i] = casing[l]
	}
	return out
}

// record bumps counts for one expansion's candidates and returns the stats
// that just crossed the suggestion threshold (each surfaced exactly once).
func (s *suggestionStore) record(tool string, candidates []string, now time.Time) []*PatternStat {
	if len(candidates) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()

	var crossed []*PatternStat
	for _, c := range candidates {
		key := strings.ToLower(c)
		st, ok := s.data[key]
		if !ok {
			if len(s.data) >= maxTrackedPatterns {
				s.evictOne()
			}
			st = &PatternStat{Pattern: c}
			s.data[key] = st
		}
		st.Expansions++
		st.LastSeen = now
		if tool != "" && !containsStr(st.Tools, tool) {
			st.Tools = append(st.Tools, tool)
		}
		if st.Expansions >= suggestThreshold && !st.Suggested {
			st.Suggested = true
			crossed = append(crossed, st)
		}
	}
	s.save()
	return crossed
}

// Snapshot returns tracked patterns sorted by expansion count desc (label asc
// on ties). Used by `forge compression suggestions`.
func (s *suggestionStore) Snapshot() []PatternStat {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()
	out := make([]PatternStat, 0, len(s.data))
	for _, st := range s.data {
		out = append(out, *st)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Expansions != out[b].Expansions {
			return out[a].Expansions > out[b].Expansions
		}
		return out[a].Pattern < out[b].Pattern
	})
	return out
}

// load reads the file once; absence or corruption starts fresh (the flywheel
// is advisory — never worth failing anything over).
func (s *suggestionStore) load() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.data = make(map[string]*PatternStat)
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var list []*PatternStat
	if json.Unmarshal(raw, &list) != nil {
		return
	}
	for _, st := range list {
		s.data[strings.ToLower(st.Pattern)] = st
	}
}

// save writes atomically; failures are swallowed (advisory data).
func (s *suggestionStore) save() {
	list := make([]*PatternStat, 0, len(s.data))
	for _, st := range s.data {
		list = append(list, st)
	}
	sort.Slice(list, func(a, b int) bool { return list[a].Pattern < list[b].Pattern })
	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) != nil {
		return
	}
	_ = os.Rename(tmp, s.path)
}

// evictOne drops the lowest-count (oldest on ties) unsuggested entry, or the
// overall lowest when everything has been suggested. Caller holds s.mu.
func (s *suggestionStore) evictOne() {
	var victim string
	var vst *PatternStat
	for k, st := range s.data {
		if vst == nil ||
			(st.Suggested == vst.Suggested && (st.Expansions < vst.Expansions ||
				(st.Expansions == vst.Expansions && st.LastSeen.Before(vst.LastSeen)))) ||
			(!st.Suggested && vst.Suggested) {
			victim, vst = k, st
		}
	}
	if victim != "" {
		delete(s.data, victim)
	}
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// recordExpansionFeedback runs the flywheel for one successful expansion:
// count the pre-mined candidates and surface newly-crossed suggestions via
// audit + log. Candidates are extracted once by the caller and shared with
// the context_expanded event.
func (r *Runtime) recordExpansionFeedback(ctx context.Context, tool string, candidates []string) {
	if r.feedback == nil {
		return
	}
	crossed := r.feedback.record(tool, candidates, time.Now())
	for _, st := range crossed {
		r.debugf("keep_patterns suggestion", map[string]any{
			"pattern": st.Pattern, "expansions": st.Expansions, "tools": st.Tools,
		})
		if r.audit != nil {
			r.audit(ctx, AuditEventPatternSuggested, map[string]any{
				"pattern":    st.Pattern,
				"expansions": st.Expansions,
				"tools":      st.Tools,
			})
		}
	}
}

// Suggestions exposes the tracked keep-pattern candidates (for the CLI).
func (r *Runtime) Suggestions() []PatternStat {
	if r.feedback == nil {
		return nil
	}
	return r.feedback.Snapshot()
}

// SuggestionsPath returns the flywheel file for a given store path.
func SuggestionsPath(storePath string) string {
	return filepath.Join(filepath.Dir(storePath), SuggestionsFileName)
}
