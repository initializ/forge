// Package compress wires reversible context compression (ctxzip) into the
// forge agent loop.
//
// ctxzip shrinks bulky content before it reaches the LLM — tool outputs,
// logs, JSON — and offloads everything it drops to a durable local store,
// replaced inline by a retrievable "<<ctxzip:HASH ...>>" marker. Compression
// is lossy on the wire but lossless end-to-end: the model can recover any
// original via the context_expand tool.
//
// Three integration seams, all owned by Runtime:
//
//   - AfterToolExecHook — compresses tool output once, at production time,
//     before it enters Memory. Because the compressed bytes never change
//     afterwards, the conversation prefix stays byte-stable across turns and
//     provider prompt caches keep hitting.
//   - WrapClient — an llm.Client decorator that compresses the live zone of
//     every outbound request (skipping the frozen prefix and recent turns).
//     It is deliberately deterministic: the relevance query is pinned to the
//     first user message of the session, never the latest turn, so the same
//     historic message always compresses to the same bytes.
//   - ExpandTool — the context_expand builtin that retrieves originals from
//     the store by marker hash.
//
// The store is a bbolt file (default .forge/ctxzip.db) so originals survive
// process restarts; entries expire after TTL, at which point the disk or the
// original command is the source of truth (the tool's miss message says so).
package compress

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/initializ/ctxzip/ccr"

	"github.com/initializ/forge/forge-core/runtime"
)

// AuditFunc receives compression audit events. The runner wires this to the
// AuditLogger (EmitFromContext, so correlation_id/task_id are stamped from
// ctx); nil disables audit emission. Token figures are tokenizer estimates,
// not provider-billed counts — directionally accurate for savings reporting.
type AuditFunc func(ctx context.Context, event string, fields map[string]any)

// Audit event names emitted by the compression runtime.
const (
	// AuditEventCompressed fires whenever content is compressed, from either
	// seam (tool_output hook or request wrapper). Fields: seam, tool,
	// tokens_before, tokens_after, saved_tokens, plus running totals
	// total_saved_tokens / total_compressions / total_expansions so any
	// single event shows the cumulative savings picture.
	AuditEventCompressed = "context_compressed"
	// AuditEventExpanded fires when the model retrieves offloaded content
	// via context_expand. Fields: hash, hit, bytes, plus the same running
	// totals — expansions are the "cost" side auditors net against savings.
	AuditEventExpanded = "context_expanded"
)

// Defaults for Config fields left at their zero value.
const (
	DefaultTTL                = 30 * time.Minute
	DefaultMinToolOutputChars = 2048
)

// Config configures the compression runtime.
type Config struct {
	// StorePath is the bbolt file for the CCR store (required),
	// e.g. .forge/ctxzip.db.
	StorePath string
	// TTL is how long offloaded originals stay retrievable. Default 30m.
	TTL time.Duration
	// MinToolOutputChars is the size below which tool outputs are left
	// alone by the AfterToolExec hook. Default 2048.
	MinToolOutputChars int
	// KeepPatterns is the builder's domain vocabulary of case-insensitive
	// substrings compression must never drop (forge.yaml
	// compression.keep_patterns). Union with ctxzip's built-in error floor.
	KeepPatterns []string
	// Logger is optional; nil disables logging.
	Logger runtime.Logger
	// Audit is optional; nil disables audit emission. See AuditFunc.
	Audit AuditFunc
}

// Runtime owns the shared CCR store and produces the hook, client wrapper,
// and expand tool that plug into the agent loop.
type Runtime struct {
	store    *ccr.BoltStore
	minSize  int
	keep     []string
	logger   runtime.Logger
	audit    AuditFunc
	feedback *suggestionStore

	// recent remembers marker hashes this process emitted — for the expand
	// tool's prefix resolution when the model transcribes a hash imperfectly
	// (observed live), and mapping each hash to the tokens its compression
	// saved so the client wrapper can credit REALIZED savings every time the
	// marker rides in an outbound request (savings compound per resend, not
	// per compression). Bounded to maxRecentMarkers, evicting oldest-emitted
	// first.
	mu          sync.Mutex
	recent      map[string]int64
	recentOrder []string
	totals      SavingsTotals
	// perInvocation accumulates savings keyed by correlation ID so
	// invocation_complete can report this invocation's savings without
	// cross-contamination from concurrent tasks. Entries are popped by
	// TakeInvocationTotals at the invocation's response boundary; as a
	// leak backstop for any emission path that misses the pop, the map is
	// bounded to maxInvocationBuckets with oldest-touched eviction.
	perInvocation map[string]*invocationBucket
}

// invocationBucket carries one invocation's savings plus a touch time for
// oldest-first eviction at the cap.
type invocationBucket struct {
	totals  SavingsTotals
	touched time.Time
}

// Bounds for the Runtime's process-lifetime maps (PR #241 review).
const (
	// maxInvocationBuckets bounds perInvocation. Normal operation pops each
	// bucket at invocation end, so this only matters if an emission path
	// misses the pop — 1024 in-flight correlation IDs is far beyond any
	// realistic concurrency.
	maxInvocationBuckets = 1024
	// maxRecentMarkers bounds the prefix-resolution set. 2048 markers is
	// hours of heavy compression; older hashes are no longer "recent" and
	// exact-hash retrieval from the store still works without them.
	maxRecentMarkers = 2048
)

// SavingsTotals is the process-lifetime savings picture. Token figures are
// tokenizer estimates.
type SavingsTotals struct {
	// Compressions is how many times content was compressed (either seam).
	Compressions int64
	// SavedTokens is the cumulative per-EVENT token reduction — counted once
	// per compression, at compression time.
	SavedTokens int64
	// WireSavedTokens is the cumulative REALIZED reduction: every time a
	// marker rides in an outbound LLM request, that marker's saved tokens are
	// tokens this request did not send. A tool output compressed once but
	// resent in history across ten calls saves its delta ten times — this is
	// the number that matches the provider's bill (live finding: an
	// invocation reporting 1,257 event-saved tokens had actually avoided
	// ~31K billed tokens through history compounding).
	WireSavedTokens int64
	// Expansions / ExpansionMisses count context_expand retrievals — the
	// cost side to net against savings.
	Expansions      int64
	ExpansionMisses int64
}

// Totals returns a snapshot of the cumulative savings picture.
func (r *Runtime) Totals() SavingsTotals {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.totals
}

// TakeInvocationTotals pops and returns the savings accumulated under the
// ctx's correlation ID. Call it exactly once, at the invocation's response
// boundary (invocation_complete emission); subsequent calls for the same
// invocation return zeros. Safe under concurrent invocations — each
// correlation ID accumulates independently.
func (r *Runtime) TakeInvocationTotals(ctx context.Context) SavingsTotals {
	cid := runtime.CorrelationIDFromContext(ctx)
	if cid == "" {
		return SavingsTotals{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if b, ok := r.perInvocation[cid]; ok {
		delete(r.perInvocation, cid)
		return b.totals
	}
	return SavingsTotals{}
}

// bumpInvocation accumulates into the ctx's per-invocation bucket, evicting
// the oldest-touched bucket when the map exceeds maxInvocationBuckets (leak
// backstop — normal operation pops buckets at invocation end). Caller holds
// r.mu.
func (r *Runtime) bumpInvocation(ctx context.Context, f func(*SavingsTotals)) {
	cid := runtime.CorrelationIDFromContext(ctx)
	if cid == "" {
		return
	}
	if r.perInvocation == nil {
		r.perInvocation = make(map[string]*invocationBucket)
	}
	b, ok := r.perInvocation[cid]
	if !ok {
		if len(r.perInvocation) >= maxInvocationBuckets {
			var oldestKey string
			var oldest time.Time
			for k, v := range r.perInvocation {
				if oldestKey == "" || v.touched.Before(oldest) {
					oldestKey, oldest = k, v.touched
				}
			}
			delete(r.perInvocation, oldestKey)
		}
		b = &invocationBucket{}
		r.perInvocation[cid] = b
	}
	b.touched = time.Now()
	f(&b.totals)
}

// recordCompression accumulates savings and emits AuditEventCompressed with
// per-event figures plus the running totals.
func (r *Runtime) recordCompression(ctx context.Context, seam, tool string, before, after int) {
	r.mu.Lock()
	r.totals.Compressions++
	r.totals.SavedTokens += int64(before - after)
	r.bumpInvocation(ctx, func(t *SavingsTotals) {
		t.Compressions++
		t.SavedTokens += int64(before - after)
	})
	t := r.totals
	r.mu.Unlock()

	if r.audit == nil {
		return
	}
	r.audit(ctx, AuditEventCompressed, map[string]any{
		"seam":               seam,
		"tool":               tool,
		"tokens_before":      before,
		"tokens_after":       after,
		"saved_tokens":       before - after,
		"total_saved_tokens": t.SavedTokens,
		"total_compressions": t.Compressions,
		"total_expansions":   t.Expansions,
	})
}

// recordExpansion accumulates retrieval stats and emits AuditEventExpanded.
func (r *Runtime) recordExpansion(ctx context.Context, hash string, hit bool, bytes int) {
	r.mu.Lock()
	r.totals.Expansions++
	if !hit {
		r.totals.ExpansionMisses++
	}
	r.bumpInvocation(ctx, func(t *SavingsTotals) {
		t.Expansions++
		if !hit {
			t.ExpansionMisses++
		}
	})
	t := r.totals
	r.mu.Unlock()

	if r.audit == nil {
		return
	}
	r.audit(ctx, AuditEventExpanded, map[string]any{
		"hash":                   hash,
		"hit":                    hit,
		"bytes":                  bytes,
		"total_saved_tokens":     t.SavedTokens,
		"total_expansions":       t.Expansions,
		"total_expansion_misses": t.ExpansionMisses,
	})
}

// rememberMarkers records emitted marker hashes with the tokens their
// compression saved (savedTokens is split evenly across the transform's
// markers), evicting the oldest-emitted entries beyond maxRecentMarkers.
func (r *Runtime) rememberMarkers(hashes []string, savedTokens int64) {
	if len(hashes) == 0 {
		return
	}
	perMarker := savedTokens / int64(len(hashes))
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recent == nil {
		r.recent = make(map[string]int64)
	}
	for _, h := range hashes {
		if _, ok := r.recent[h]; ok {
			continue
		}
		r.recent[h] = perMarker
		r.recentOrder = append(r.recentOrder, h)
		if len(r.recentOrder) > maxRecentMarkers {
			delete(r.recent, r.recentOrder[0])
			r.recentOrder = r.recentOrder[1:]
		}
	}
}

// recordWireSavings credits the realized savings of one outbound request:
// every marker present in the request's message contents represents tokens
// this request did not send. Called by the client wrapper on every Chat /
// ChatStream, including calls where nothing new was compressed — history
// markers keep saving on every resend, which is where most realized savings
// live.
func (r *Runtime) recordWireSavings(ctx context.Context, contents []string) {
	var saved int64
	r.mu.Lock()
	for _, c := range contents {
		if !ccr.HasMarker(c) {
			continue
		}
		for _, h := range ccr.ExtractHashes(c) {
			saved += r.recent[h]
		}
	}
	if saved > 0 {
		r.totals.WireSavedTokens += saved
		r.bumpInvocation(ctx, func(t *SavingsTotals) {
			t.WireSavedTokens += saved
		})
	}
	r.mu.Unlock()
}

// resolvePrefix returns the unique remembered hash starting with prefix, or
// "" when the prefix is too short, unknown, or ambiguous.
func (r *Runtime) resolvePrefix(prefix string) string {
	if len(prefix) < 6 {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var found string
	for h := range r.recent {
		if strings.HasPrefix(h, prefix) {
			if found != "" {
				return "" // ambiguous
			}
			found = h
		}
	}
	return found
}

// New opens the durable store and returns a Runtime. Call Close on shutdown.
func New(cfg Config) (*Runtime, error) {
	if cfg.StorePath == "" {
		return nil, fmt.Errorf("compress: Config.StorePath is required")
	}
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultTTL
	}
	if cfg.MinToolOutputChars <= 0 {
		cfg.MinToolOutputChars = DefaultMinToolOutputChars
	}
	// bbolt creates the DB file but not its parent directory; on a fresh
	// project .forge/ does not exist yet, so create it (0700 — originals can
	// hold sensitive tool output).
	if dir := filepath.Dir(cfg.StorePath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("compress: creating store dir: %w", err)
		}
	}
	store, err := ccr.NewBoltStore(ccr.BoltConfig{Path: cfg.StorePath, TTL: cfg.TTL})
	if err != nil {
		return nil, fmt.Errorf("compress: opening store: %w", err)
	}
	return &Runtime{
		store:    store,
		minSize:  cfg.MinToolOutputChars,
		keep:     cfg.KeepPatterns,
		logger:   cfg.Logger,
		audit:    cfg.Audit,
		feedback: newSuggestionStore(SuggestionsPath(cfg.StorePath)),
	}, nil
}

// Close releases the underlying store.
func (r *Runtime) Close() error {
	return r.store.Close()
}

// Store exposes the CCR store (used by tests and diagnostics).
func (r *Runtime) Store() ccr.Store { return r.store }

func (r *Runtime) debugf(msg string, fields map[string]any) {
	if r.logger != nil {
		r.logger.Debug(msg, fields)
	}
}
