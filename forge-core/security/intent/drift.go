package intent

import (
	"fmt"
	"math"
	"sync"
)

// DriftConfig configures the R7 (#214) rolling-window drift signal.
// Where the R3 alignment check is a per-call policy gate, drift is
// longitudinal telemetry — it watches the sequence of alignment
// scores accumulating for a task and flags trends that suggest the
// agent is progressively wandering from the stated intent.
type DriftConfig struct {
	// Enabled turns the analyzer on. When false, RecordAndCheck is
	// a no-op and no drift signals fire.
	Enabled bool

	// Window is the number of most-recent scores considered by the
	// rolling-mean test. Must be ≥ 2 — a window of 1 can't
	// distinguish "trending down" from "just low."
	Window int

	// DriftThreshold is the mean-score floor. When the rolling
	// window mean falls strictly below this value, drift enters
	// the "mean_below_threshold" state.
	DriftThreshold float64

	// MonotoneN, when non-zero, additionally trips drift when the
	// last N scores are strictly decreasing (even if the mean is
	// still above DriftThreshold). Catches slow-boil patterns
	// where each individual step is small but cumulative drift is
	// large. Zero disables the monotone check.
	MonotoneN int
}

// Validate returns an error when DriftConfig would produce
// nonsensical decisions. Called at Engine construction so runners
// fail startup rather than at first RecordAndCheck.
func (c DriftConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.Window < 2 {
		return fmt.Errorf("intent_drift: window %d < 2 (can't distinguish trend from magnitude)", c.Window)
	}
	if c.DriftThreshold < -1 || c.DriftThreshold > 1 {
		return fmt.Errorf("intent_drift: drift_threshold %.3f outside [-1,1]", c.DriftThreshold)
	}
	if c.MonotoneN < 0 || (c.MonotoneN > 0 && c.MonotoneN < 2) {
		return fmt.Errorf("intent_drift: monotone_n %d invalid (must be 0 or ≥ 2)", c.MonotoneN)
	}
	// The ring holds only Window scores. If MonotoneN > Window,
	// checkMonotoneDown's `len(scores) < n` guard is permanently
	// true and the monotone check silently never fires — an
	// operator would believe they have slow-drift detection while
	// having none. Reject rather than clamp so the misconfig is
	// visible at startup.
	if c.MonotoneN > c.Window {
		return fmt.Errorf("intent_drift: monotone_n %d > window %d (ring never accumulates enough scores for the monotone check to trip; either raise window or lower monotone_n)",
			c.MonotoneN, c.Window)
	}
	return nil
}

// DriftSignal describes a drift-state transition. Populated on
// engine.Score's return value when a state change happens on that
// call; nil otherwise. The runner emits an `intent_drift` audit
// event iff Signal != nil.
//
// State-transition emission (rather than "emit every call while in
// drift") keeps the audit stream from flooding — one event when the
// task first crosses into drift, one when it recovers.
type DriftSignal struct {
	// Severity is the human-readable classification. One of
	// "mean_below_threshold", "monotone_decrease", "both", or
	// "recovered" (transition OUT of drift).
	Severity string

	// Mean is the rolling-window mean of the last Window scores at
	// the moment the signal fired. Included on both entry and
	// recovery transitions for the audit event.
	Mean float64

	// Window is the number of scores in the mean.
	Window int

	// Transition is "entered" for on-entry signals or "recovered"
	// for the exit signal. Makes SIEM queries "when did I go into
	// drift?" trivial.
	Transition string
}

// driftState is the per-task ring buffer + last-known-in-drift flag.
type driftState struct {
	// scores is a ring buffer (append + trim); order oldest→newest.
	scores  []float64
	inDrift bool
}

// analyzer is the drift coordinator. Owned by the Engine and gated
// by the same mutex as the intent map (locking is coarse but
// score-recording is cheap; the per-task ring stays small).
type analyzer struct {
	cfg DriftConfig
	mu  sync.Mutex
	// keyed by taskID; entries live as long as the task's intent
	// entry — Forget also clears drift state for the task.
	states map[string]*driftState
}

func newAnalyzer(cfg DriftConfig) *analyzer {
	if !cfg.Enabled {
		return nil // sentinel: engine treats nil analyzer as "off"
	}
	return &analyzer{
		cfg:    cfg,
		states: make(map[string]*driftState),
	}
}

// enabled reports whether the analyzer is armed. Nil-safe.
func (a *analyzer) enabled() bool { return a != nil && a.cfg.Enabled }

// record inserts `score` into the task's ring and returns a
// DriftSignal iff the state changed (entered or recovered from
// drift). NaN scores (from R3 fail-closed paths) are recorded as-is
// so the ring reflects the operator's true observation history; the
// trip checks handle NaN by treating it as below-threshold (the
// engine failed closed for a reason).
func (a *analyzer) record(taskID string, score float64) *DriftSignal {
	if !a.enabled() {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	st, ok := a.states[taskID]
	if !ok {
		st = &driftState{scores: make([]float64, 0, a.cfg.Window)}
		a.states[taskID] = st
	}
	st.scores = append(st.scores, score)
	if len(st.scores) > a.cfg.Window {
		st.scores = st.scores[len(st.scores)-a.cfg.Window:]
	}

	// Not enough history to make a determination.
	if len(st.scores) < a.cfg.Window {
		return nil
	}

	meanBelow, mean := a.checkMeanBelow(st.scores)
	monotoneDown := a.checkMonotoneDown(st.scores)
	inDriftNow := meanBelow || monotoneDown

	if inDriftNow && !st.inDrift {
		st.inDrift = true
		return &DriftSignal{
			Severity:   driftSeverity(meanBelow, monotoneDown),
			Mean:       mean,
			Window:     a.cfg.Window,
			Transition: "entered",
		}
	}
	if !inDriftNow && st.inDrift {
		st.inDrift = false
		return &DriftSignal{
			Severity:   "recovered",
			Mean:       mean,
			Window:     a.cfg.Window,
			Transition: "recovered",
		}
	}
	return nil
}

// forget clears any drift state for the given task.
func (a *analyzer) forget(taskID string) {
	if !a.enabled() {
		return
	}
	a.mu.Lock()
	delete(a.states, taskID)
	a.mu.Unlock()
}

// checkMeanBelow returns (crosses, mean). Treats NaN scores as 0
// for the mean so a fail-closed emit contributes to drift rather
// than silently disappearing.
func (a *analyzer) checkMeanBelow(scores []float64) (bool, float64) {
	if len(scores) == 0 {
		return false, 0
	}
	var sum float64
	for _, s := range scores {
		if math.IsNaN(s) {
			// Treat fail-closed observations as maximally-mis
			// aligned for drift purposes. Symmetric with the R3
			// hook, which returns Deny in the same case.
			sum += -1
			continue
		}
		sum += s
	}
	mean := sum / float64(len(scores))
	return mean < a.cfg.DriftThreshold, mean
}

// checkMonotoneDown returns true iff the last MonotoneN scores are
// strictly decreasing. Uses the tail of the ring — a longer window
// than MonotoneN still only checks the last MonotoneN entries.
func (a *analyzer) checkMonotoneDown(scores []float64) bool {
	n := a.cfg.MonotoneN
	if n < 2 {
		return false
	}
	if len(scores) < n {
		return false
	}
	tail := scores[len(scores)-n:]
	for i := 1; i < n; i++ {
		// NaN comparisons are false → a NaN in the tail breaks
		// the "strictly decreasing" claim, which is the correct
		// conservative behavior.
		if !(tail[i] < tail[i-1]) {
			return false
		}
	}
	return true
}

// driftSeverity maps the (meanBelow, monotoneDown) combination to
// the audit-facing severity token.
func driftSeverity(meanBelow, monotoneDown bool) string {
	switch {
	case meanBelow && monotoneDown:
		return "both"
	case meanBelow:
		return "mean_below_threshold"
	case monotoneDown:
		return "monotone_decrease"
	default:
		return ""
	}
}
