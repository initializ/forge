// Package intent implements governance R3 — the intent-alignment
// policy check.
//
// The MUST from the governance framework: every action is evaluated
// against a policy that considers both the action itself AND its
// alignment with the stated agent intent. Forge's pre-R3 policy
// evaluation (guardrails, egress, admission) covered the "action
// itself" half — this package fills the second half with a per-tool-
// call cosine-similarity check between the user's stated intent and
// the tool call the LLM is about to make.
//
// Wire shape:
//
//  1. On tasks/send entry, the A2A handler calls Engine.RegisterIntent
//     with the task ID + the first user message text. The engine
//     computes and caches the intent embedding once per task.
//
//  2. On every BeforeToolExec hook, the runner calls Engine.Score
//     with the task ID + tool description + tool-input JSON. The
//     engine embeds the concatenated action text (cached per hash),
//     computes cosine similarity against the intent embedding,
//     compares against the configured thresholds, and returns a
//     Result.
//
//  3. The hook consumer emits the Result as an `intent_alignment`
//     audit event and, on DecisionDeny, aborts the tool call.
//
// Fail-closed posture: when Config.Enabled is true, an unavailable
// embedder (config error, transient network) causes Score to return
// DecisionDeny with reason="embedder unavailable". Governance-
// critical: silent bypass is not a valid failure mode.
package intent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/initializ/forge/forge-core/llm"
)

// Config carries the operator-tunable knobs for the alignment check.
// Populated from forge.yaml security.intent_alignment. See
// docs/security/intent-alignment.md.
type Config struct {
	// Enabled turns the check on. Default false. When false, the hook
	// short-circuits — no embedder calls, no audit emit.
	Enabled bool

	// Threshold is the SOFT similarity floor. Scores strictly below
	// threshold but at-or-above HardThreshold produce DecisionWarn.
	// Sensible default 0.5 — operators SHOULD start warn-only for a
	// sprint to gather the score distribution before turning deny on.
	Threshold float64

	// HardThreshold is the HARD floor. Scores strictly below produce
	// DecisionDeny. Set equal to Threshold to disable the warn tier;
	// set to a negative value (e.g. -1) to run warn-only during the
	// initial rollout. Sensible default 0.3.
	HardThreshold float64

	// CacheSize is the max number of action-side embeddings to
	// remember in the LRU. Zero disables the action cache (still
	// caches the per-task intent). 1024 is a reasonable default.
	CacheSize int

	// IntentTTL controls how long a task's intent embedding is
	// kept in memory before it's evicted. Zero → 1 hour default.
	IntentTTL time.Duration
}

// Validate returns an error when Config's values would produce
// nonsensical decisions. Called at Engine construction so the
// runner fails startup rather than at first Score.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	// Cosine similarity is defined on [-1, 1]; a negative
	// hard_threshold is the legitimate "warn-only" configuration
	// (no score can be below -1, so deny never fires). The old
	// [0,1] range rejected that configuration on startup.
	if c.Threshold < -1 || c.Threshold > 1 {
		return fmt.Errorf("intent_alignment: threshold %.3f outside [-1,1]", c.Threshold)
	}
	if c.HardThreshold < -1 || c.HardThreshold > 1 {
		return fmt.Errorf("intent_alignment: hard_threshold %.3f outside [-1,1]", c.HardThreshold)
	}
	if c.HardThreshold > c.Threshold {
		return fmt.Errorf("intent_alignment: hard_threshold %.3f > threshold %.3f (would starve WARN tier)",
			c.HardThreshold, c.Threshold)
	}
	return nil
}

// Decision is the alignment engine's contribution to the policy
// evaluation. Maps 1:1 to runtime.PolicyDecision but the intent
// package doesn't import runtime (avoid a cycle: intent → runtime
// via memory → intent later); the runner adapts.
type Decision int

const (
	// DecisionAllow — score at or above the soft threshold.
	DecisionAllow Decision = iota

	// DecisionWarn — score below soft but at-or-above hard threshold.
	// The runner emits the audit event and lets the tool call proceed;
	// operators tail the audit stream to tune.
	DecisionWarn

	// DecisionDeny — score below hard threshold, or the engine
	// couldn't produce a score (fail-closed on embedder error).
	// The runner aborts the tool call.
	DecisionDeny
)

// String returns the audit-safe token.
func (d Decision) String() string {
	switch d {
	case DecisionAllow:
		return "allow"
	case DecisionWarn:
		return "warn"
	case DecisionDeny:
		return "deny"
	default:
		return "unknown"
	}
}

// Result carries the outcome of one Score call. Surfaced as fields
// on the `intent_alignment` audit event.
type Result struct {
	// Score is the cosine similarity ∈ [-1, 1]. NaN when the engine
	// couldn't produce a value (embedder error, missing intent).
	Score float64

	// Decision is the alignment engine's verdict.
	Decision Decision

	// Reason is a short human-readable classification — surfaced on
	// the audit event and, on Deny, returned as the tool-exec error.
	Reason string

	// Drift is non-nil ONLY on the tool call that transitions the
	// task into or out of drift (R7 / #214). The hook consumer
	// emits an `intent_drift` audit event when set. State-transition
	// semantics keep the audit stream from flooding across long
	// stretches of drift.
	Drift *DriftSignal
}

// Engine is the per-runtime intent-alignment coordinator. Safe for
// concurrent use — RegisterIntent + Score fire from independent
// goroutines (one per A2A request handler + one per hook fire).
type Engine struct {
	cfg      Config
	embedder llm.Embedder

	mu      sync.Mutex
	intents map[string]*intentEntry
	// action cache: sha256(action_text) → embedding vector
	actions    *lruCache
	lastGCUnix int64 // last time expired intent entries were swept, unix seconds
	nowFn      func() time.Time

	// drift is the R7 (#214) rolling-window analyzer. Nil when
	// intent_drift is disabled; the Engine treats nil as "no drift
	// tracking" so R3 keeps working without R7.
	drift *analyzer
}

type intentEntry struct {
	embedding []float32
	expiresAt time.Time
}

// New constructs an Engine with just the R3 alignment check. The
// R7 drift analyzer stays off. embedder may be nil when cfg.Enabled
// is false; enabling without an embedder returns an error.
func New(cfg Config, embedder llm.Embedder) (*Engine, error) {
	return NewWithDrift(cfg, DriftConfig{}, embedder)
}

// NewWithDrift constructs an Engine with the R3 alignment check
// AND the R7 rolling-window drift analyzer. drift.Enabled requires
// cfg.Enabled to be true (drift is derived from alignment scores).
func NewWithDrift(cfg Config, drift DriftConfig, embedder llm.Embedder) (*Engine, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := drift.Validate(); err != nil {
		return nil, err
	}
	if cfg.Enabled && embedder == nil {
		return nil, errors.New("intent_alignment: enabled but no embedder configured")
	}
	if drift.Enabled && !cfg.Enabled {
		return nil, errors.New("intent_drift: requires intent_alignment.enabled = true")
	}
	if cfg.IntentTTL == 0 {
		cfg.IntentTTL = time.Hour
	}
	e := &Engine{
		cfg:      cfg,
		embedder: embedder,
		intents:  make(map[string]*intentEntry),
		actions:  newLRUCache(cfg.CacheSize),
		nowFn:    time.Now,
		drift:    newAnalyzer(drift),
	}
	return e, nil
}

// Enabled reports whether the engine is armed. Runners check this to
// short-circuit hook wiring on unconfigured deployments.
func (e *Engine) Enabled() bool { return e != nil && e.cfg.Enabled }

// RegisterIntent records the stated intent for a task. Called from
// the A2A handler on tasks/send entry. Idempotent per task ID — the
// FIRST call wins; subsequent calls for the same task are no-ops so
// mid-conversation user messages don't overwrite the original intent.
func (e *Engine) RegisterIntent(ctx context.Context, taskID, statedIntent string) error {
	if !e.Enabled() {
		return nil
	}
	if taskID == "" || statedIntent == "" {
		return nil
	}
	e.mu.Lock()
	if _, exists := e.intents[taskID]; exists {
		e.mu.Unlock()
		return nil
	}
	e.mu.Unlock()

	resp, err := e.embedder.Embed(ctx, &llm.EmbeddingRequest{Texts: []string{statedIntent}})
	if err != nil {
		return fmt.Errorf("intent_alignment: embedding stated intent: %w", err)
	}
	if len(resp.Embeddings) == 0 {
		return errors.New("intent_alignment: embedder returned no vectors")
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	// Re-check under lock so a race between two concurrent
	// RegisterIntent calls for the same task doesn't leak two
	// embedder calls' worth of state.
	if _, exists := e.intents[taskID]; exists {
		return nil
	}
	e.intents[taskID] = &intentEntry{
		embedding: resp.Embeddings[0],
		expiresAt: e.nowFn().Add(e.cfg.IntentTTL),
	}
	e.gcExpiredLocked()
	return nil
}

// Forget removes the intent embedding AND any drift-analyzer state
// for the given task. Called from the A2A handler at session_end
// so long-running processes don't retain per-task state
// indefinitely.
func (e *Engine) Forget(taskID string) {
	if !e.Enabled() {
		return
	}
	e.mu.Lock()
	delete(e.intents, taskID)
	e.mu.Unlock()
	e.drift.forget(taskID)
}

// Score computes the alignment between the previously-registered
// stated intent for taskID and the current action (tool call).
//
// actionText is the concatenation of the tool's description and the
// LLM-supplied args JSON — chosen over tool name because tool names
// are arbitrary handles (`fn_42`) that don't reflect semantics,
// while descriptions carry what the tool DOES and args carry the
// specific values.
//
// Returns DecisionDeny when:
//   - The engine is enabled but no intent was registered for taskID
//     (guards against a hook firing before the A2A handler set up
//     the task).
//   - The embedder call fails or returns zero vectors.
//   - The score is strictly below cfg.HardThreshold.
//
// When cfg.Enabled == false, returns DecisionAllow with Score=NaN
// so the caller can distinguish "not checked" from "checked and
// passed at 1.0."
func (e *Engine) Score(ctx context.Context, taskID, actionText string) Result {
	if !e.Enabled() {
		return Result{Score: math.NaN(), Decision: DecisionAllow, Reason: "intent_alignment disabled"}
	}

	e.mu.Lock()
	entry, ok := e.intents[taskID]
	if !ok || e.nowFn().After(entry.expiresAt) {
		if ok {
			delete(e.intents, taskID)
		}
		e.mu.Unlock()
		res := Result{
			Score:    math.NaN(),
			Decision: DecisionDeny,
			Reason:   "no stated_intent registered for task (fail-closed)",
		}
		// R7: fail-closed observations feed the drift ring too, so
		// a task with a broken intent capture surfaces as drift
		// rather than invisible.
		res.Drift = e.drift.record(taskID, math.NaN())
		return res
	}
	intentVec := entry.embedding
	e.mu.Unlock()

	actionVec, err := e.embedAction(ctx, actionText)
	if err != nil {
		res := Result{
			Score:    math.NaN(),
			Decision: DecisionDeny,
			Reason:   fmt.Sprintf("embedder unavailable (fail-closed): %v", err),
		}
		res.Drift = e.drift.record(taskID, math.NaN())
		return res
	}

	sim := cosine(intentVec, actionVec)
	res := Result{Score: sim}
	switch {
	case sim < e.cfg.HardThreshold:
		res.Decision = DecisionDeny
		res.Reason = fmt.Sprintf("score %.3f below hard_threshold %.3f", sim, e.cfg.HardThreshold)
	case sim < e.cfg.Threshold:
		res.Decision = DecisionWarn
		res.Reason = fmt.Sprintf("score %.3f below threshold %.3f", sim, e.cfg.Threshold)
	default:
		res.Decision = DecisionAllow
		res.Reason = fmt.Sprintf("score %.3f above threshold %.3f", sim, e.cfg.Threshold)
	}

	// R7: feed the score into the rolling-window drift analyzer and
	// attach any state-transition signal to the result. No-op when
	// the analyzer is disabled.
	res.Drift = e.drift.record(taskID, sim)
	return res
}

// embedAction returns the cached embedding for actionText, computing
// and inserting on miss. Cache key is sha256 of the raw actionText —
// deterministic across process restarts is not required (the cache
// is in-memory only), but hashing avoids storing potentially large
// action strings in the LRU keys.
func (e *Engine) embedAction(ctx context.Context, actionText string) ([]float32, error) {
	key := actionKey(actionText)
	if e.actions != nil {
		if v, ok := e.actions.Get(key); ok {
			return v, nil
		}
	}
	resp, err := e.embedder.Embed(ctx, &llm.EmbeddingRequest{Texts: []string{actionText}})
	if err != nil {
		return nil, err
	}
	if len(resp.Embeddings) == 0 {
		return nil, errors.New("embedder returned no vectors")
	}
	vec := resp.Embeddings[0]
	if e.actions != nil {
		e.actions.Put(key, vec)
	}
	return vec, nil
}

// gcExpiredLocked evicts expired intent entries. Called from
// RegisterIntent under lock — no separate GC goroutine (the memory
// churn of one entry per task is bounded by the active-task count).
func (e *Engine) gcExpiredLocked() {
	now := e.nowFn()
	if now.Unix()-e.lastGCUnix < 60 {
		return
	}
	e.lastGCUnix = now.Unix()
	for k, entry := range e.intents {
		if now.After(entry.expiresAt) {
			delete(e.intents, k)
		}
	}
}

// actionKey hashes the raw action text so the LRU stores fixed-size
// keys regardless of tool-input size.
func actionKey(actionText string) string {
	sum := sha256.Sum256([]byte(actionText))
	return hex.EncodeToString(sum[:])
}

// cosine computes cosine similarity ∈ [-1, 1] between two vectors.
// Returns 0 when either vector is zero-length or magnitudes are
// zero (both cases are invalid inputs — a downstream comparison
// against a positive threshold will DENY, which is the correct
// fail-closed behavior).
func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
