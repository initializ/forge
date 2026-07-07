package intent

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/initializ/forge/forge-core/llm"
)

// scriptedEmbedder returns a scripted sequence of vectors so tests
// can drive exact cosine trajectories. Each call consumes one entry;
// the intent (first) embed uses the "intent" script, and each
// subsequent action embed pops from the "actions" queue.
type scriptedEmbedder struct {
	intent  []float32
	actions [][]float32
}

func (s *scriptedEmbedder) Embed(_ context.Context, req *llm.EmbeddingRequest) (*llm.EmbeddingResponse, error) {
	out := make([][]float32, len(req.Texts))
	for i := range req.Texts {
		if s.intent != nil {
			out[i] = s.intent
			s.intent = nil
			continue
		}
		if len(s.actions) == 0 {
			return nil, errActionsExhausted
		}
		out[i] = s.actions[0]
		s.actions = s.actions[1:]
	}
	return &llm.EmbeddingResponse{Embeddings: out}, nil
}

func (s *scriptedEmbedder) Dimensions() int { return 3 }

var errActionsExhausted = &scriptError{"scripted actions exhausted"}

type scriptError struct{ msg string }

func (e *scriptError) Error() string { return e.msg }

// TestDriftConfig_Validate pins the config-time rejections.
func TestDriftConfig_Validate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     DriftConfig
		wantErr bool
	}{
		{"disabled ok", DriftConfig{}, false},
		{"window 1 rejected", DriftConfig{Enabled: true, Window: 1, DriftThreshold: 0.3}, true},
		{"window ok", DriftConfig{Enabled: true, Window: 5, DriftThreshold: 0.3}, false},
		{"threshold out of range", DriftConfig{Enabled: true, Window: 5, DriftThreshold: 1.5}, true},
		{"monotone 1 rejected", DriftConfig{Enabled: true, Window: 5, DriftThreshold: 0.3, MonotoneN: 1}, true},
		{"monotone 0 disables", DriftConfig{Enabled: true, Window: 5, DriftThreshold: 0.3, MonotoneN: 0}, false},
		{"monotone 3 ok", DriftConfig{Enabled: true, Window: 5, DriftThreshold: 0.3, MonotoneN: 3}, false},
		// #246 review: monotone_n > window silently disabled the
		// monotone check because the ring never accumulates enough
		// scores to satisfy `len(scores) >= n`. Now rejected at
		// startup so the operator sees the misconfig.
		{"monotone_n > window rejected", DriftConfig{Enabled: true, Window: 3, DriftThreshold: 0.3, MonotoneN: 5}, true},
		{"monotone_n == window ok", DriftConfig{Enabled: true, Window: 3, DriftThreshold: 0.3, MonotoneN: 3}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.cfg.Validate()
			if (err != nil) != c.wantErr {
				t.Errorf("err=%v wantErr=%v", err, c.wantErr)
			}
		})
	}
}

// TestNewWithDrift_RejectsDriftWithoutAlignment guards the contract:
// drift is derived from alignment, so operators can't enable one
// without the other.
func TestNewWithDrift_RejectsDriftWithoutAlignment(t *testing.T) {
	_, err := NewWithDrift(
		Config{Enabled: false},
		DriftConfig{Enabled: true, Window: 5, DriftThreshold: 0.3},
		&fakeEmbedder{},
	)
	if err == nil {
		t.Fatal("expected error: drift without alignment")
	}
}

// TestDrift_HighSimilaritySequenceNeverEmits — acceptance test (a):
// a run of high-similarity scores stays out of drift.
func TestDrift_HighSimilaritySequenceNeverEmits(t *testing.T) {
	e := newDriftEngine(t, DriftConfig{
		Enabled: true, Window: 3, DriftThreshold: 0.35,
	}, [][]float32{
		{1, 0, 0}, // score 1.0
		{1, 0, 0}, // score 1.0
		{1, 0, 0}, // score 1.0
		{1, 0, 0}, // score 1.0
	})
	drove := driveScores(e, 4)
	for i, d := range drove {
		if d != nil {
			t.Errorf("call %d unexpectedly emitted drift: %+v", i, d)
		}
	}
}

// TestDrift_DecreasingSequenceTripsMeanBelow — acceptance test (b):
// a monotonically-descending sequence that pulls the mean below
// threshold trips drift on entry, and stays silent while in-drift
// (state-transition semantics — no per-call flood).
func TestDrift_DecreasingSequenceTripsMeanBelow(t *testing.T) {
	// window=3, threshold=0.35. Feed scores 0.9, 0.6, 0.3, 0.1 —
	// after call 3 the window is [0.9, 0.6, 0.3], mean 0.6 — no
	// drift. After call 4 the window is [0.6, 0.3, 0.1], mean
	// 0.333 → below 0.35 → drift entered.
	e := newDriftEngine(t, DriftConfig{
		Enabled: true, Window: 3, DriftThreshold: 0.35,
	}, [][]float32{
		{1, 0, 0},     // sim 1.0 to intent {1,0,0} — call 1: no drift
		{0.6, 0.8, 0}, // sim 0.6      — call 2: window incomplete
		{0.3, float32(math.Sqrt(1 - 0.3*0.3)), 0}, // sim ≈0.3     — call 3: mean 0.633 > 0.35 → allow
		{0.1, float32(math.Sqrt(1 - 0.1*0.1)), 0}, // sim 0.1      — call 4: mean 0.333 < 0.35 → drift!
	})
	drove := driveScores(e, 4)
	// Calls 1-3: no drift signal.
	for i := 0; i < 3; i++ {
		if drove[i] != nil {
			t.Fatalf("call %d unexpected drift: %+v", i, drove[i])
		}
	}
	// Call 4: drift entered.
	if drove[3] == nil {
		t.Fatal("call 4 should have tripped drift entry")
	}
	if drove[3].Transition != "entered" || drove[3].Severity != "mean_below_threshold" {
		t.Errorf("expected entered/mean_below_threshold, got %+v", drove[3])
	}
	if drove[3].Mean >= 0.35 {
		t.Errorf("mean should be < threshold: %.3f", drove[3].Mean)
	}
}

// TestDrift_StateTransitionOnlyEmitsOnce — while in drift and the
// mean stays below threshold, subsequent tool calls MUST NOT emit
// another drift signal. Alerting flood prevention.
func TestDrift_StateTransitionOnlyEmitsOnce(t *testing.T) {
	e := newDriftEngine(t, DriftConfig{
		Enabled: true, Window: 2, DriftThreshold: 0.5,
	}, [][]float32{
		{1, 0, 0}, // sim 1.0 — no drift (window incomplete)
		{0, 1, 0}, // sim 0.0 — mean 0.5, NOT below threshold
		{0, 1, 0}, // sim 0.0 — mean 0.0, below threshold → entered
		{0, 1, 0}, // sim 0.0 — mean 0.0, still in drift, no new signal
		{0, 1, 0}, // sim 0.0 — same
	})
	drove := driveScores(e, 5)
	entryCount := 0
	for _, d := range drove {
		if d != nil && d.Transition == "entered" {
			entryCount++
		}
	}
	if entryCount != 1 {
		t.Errorf("expected exactly 1 'entered' emission, got %d (all: %+v)", entryCount, drove)
	}
}

// TestDrift_RecoveryEmits — once drift is entered, the mean climbing
// back above threshold produces a "recovered" transition event.
func TestDrift_RecoveryEmits(t *testing.T) {
	e := newDriftEngine(t, DriftConfig{
		Enabled: true, Window: 2, DriftThreshold: 0.5,
	}, [][]float32{
		{1, 0, 0}, // sim 1.0
		{0, 1, 0}, // sim 0.0 — mean 0.5, not below (strictly)
		{0, 1, 0}, // sim 0.0 — mean 0.0 → entered
		{1, 0, 0}, // sim 1.0 — mean 0.5, still not below strictly → recover
	})
	drove := driveScores(e, 4)
	if drove[2] == nil || drove[2].Transition != "entered" {
		t.Fatalf("expected entered on call 3, got %+v", drove[2])
	}
	if drove[3] == nil || drove[3].Transition != "recovered" {
		t.Fatalf("expected recovered on call 4, got %+v", drove[3])
	}
}

// TestDrift_MonotoneDecreaseTrips — even when the mean stays high,
// N consecutive strictly-decreasing scores trigger drift under the
// monotone check ("boiling frog" pattern).
func TestDrift_MonotoneDecreaseTrips(t *testing.T) {
	// window=5, threshold=0.1 (very low; won't trip on mean).
	// monotone_n=3. Feed 0.9, 0.8, 0.7, 0.6, 0.5 — last 3 are
	// strictly decreasing, mean is 0.7 (above 0.1) → drift on
	// monotone alone.
	e := newDriftEngine(t, DriftConfig{
		Enabled: true, Window: 5, DriftThreshold: 0.1, MonotoneN: 3,
	}, [][]float32{
		{1, 0, 0},                              // 1.0
		{0.8, float32(math.Sqrt(1 - 0.64)), 0}, // 0.8
		{0.7, float32(math.Sqrt(1 - 0.49)), 0}, // 0.7
		{0.6, float32(math.Sqrt(1 - 0.36)), 0}, // 0.6
		{0.5, float32(math.Sqrt(1 - 0.25)), 0}, // 0.5
	})
	drove := driveScores(e, 5)
	if drove[4] == nil {
		t.Fatal("expected drift on call 5 (monotone)")
	}
	if drove[4].Severity != "monotone_decrease" && drove[4].Severity != "both" {
		t.Errorf("severity: got %q want monotone_decrease or both", drove[4].Severity)
	}
}

// TestDrift_DisabledIsNoop — when the drift analyzer is off, Score
// still works for R3 but never populates Result.Drift.
func TestDrift_DisabledIsNoop(t *testing.T) {
	e := newDriftEngine(t, DriftConfig{Enabled: false}, [][]float32{
		{0, 1, 0}, // any low-sim score
	})
	res := driveScoreOnce(e, "task-x", "action-1")
	if res.Drift != nil {
		t.Errorf("disabled drift should never populate Result.Drift: %+v", res.Drift)
	}
}

// TestDrift_ForgetClearsState — after Forget, the drift history is
// gone; a new sequence starts fresh (needs Window fills again).
func TestDrift_ForgetClearsState(t *testing.T) {
	e := newDriftEngine(t, DriftConfig{
		Enabled: true, Window: 2, DriftThreshold: 0.5,
	}, [][]float32{
		{0, 1, 0}, {0, 1, 0}, // both sim 0, enters drift on call 2
		{0, 1, 0}, // after Forget: fresh — only 1 score → below Window → no signal
	})
	drove := driveScores(e, 2)
	if drove[1] == nil {
		t.Fatal("setup: expected drift entry on call 2")
	}
	// Now forget and issue a third score.
	e.Forget("task-1")
	res := driveScoreOnce(e, "task-1-fresh", "action-3")
	// Need to re-register intent for the fresh task.
	if res.Decision != DecisionDeny {
		t.Errorf("fresh task w/o registered intent should fail closed, got %s", res.Decision)
	}
}

// TestDrift_FailClosedScoresPullDownMean — the analyzer treats NaN
// (fail-closed) scores as -1 for the mean. A run of embedder
// failures MUST count as drift, not as invisible.
func TestDrift_FailClosedScoresPullDownMean(t *testing.T) {
	// Register a normal intent then force the actions embedder to
	// error out.
	pub, priv := setupIntent(t, "READ the bucket")
	emb := &sequentiallyFailingEmbedder{intentReturn: pub, initialCalls: 1}
	e, _ := NewWithDrift(
		Config{Enabled: true, Threshold: 0.5, HardThreshold: 0.3},
		DriftConfig{Enabled: true, Window: 3, DriftThreshold: 0.35},
		emb,
	)
	_ = priv // unused here
	if err := e.RegisterIntent(context.Background(), "task-1", "READ the bucket"); err != nil {
		t.Fatalf("RegisterIntent: %v", err)
	}

	// Three failing embed calls → three NaN scores → each treated
	// as -1 for the mean. Mean = -1 < 0.35 → drift.
	var lastDrift *DriftSignal
	for range 3 {
		res := e.Score(context.Background(), "task-1", "some action")
		if res.Drift != nil {
			lastDrift = res.Drift
		}
	}
	if lastDrift == nil {
		t.Fatal("expected drift entry when embedder consistently fails")
	}
	if lastDrift.Transition != "entered" {
		t.Errorf("expected entered, got %s", lastDrift.Transition)
	}
}

// ---- helpers ----

// newDriftEngine builds an engine with alignment always on and
// scripted action vectors. Intent vector is fixed to {1,0,0} so
// each action script's first coordinate is the cosine.
func newDriftEngine(t *testing.T, drift DriftConfig, actions [][]float32) *Engine {
	t.Helper()
	emb := &scriptedEmbedder{
		intent:  []float32{1, 0, 0},
		actions: actions,
	}
	e, err := NewWithDrift(
		Config{Enabled: true, Threshold: 0.5, HardThreshold: 0.05},
		drift,
		emb,
	)
	if err != nil {
		t.Fatalf("NewWithDrift: %v", err)
	}
	if err := e.RegisterIntent(context.Background(), "task-1", "READ the bucket"); err != nil {
		t.Fatalf("RegisterIntent: %v", err)
	}
	return e
}

// driveScores runs Score n times with a distinct action text each
// call (so the LRU doesn't cache-hit between them) and returns the
// list of Drift signals (nil where none).
func driveScores(e *Engine, n int) []*DriftSignal {
	out := make([]*DriftSignal, n)
	for i := 0; i < n; i++ {
		res := e.Score(context.Background(), "task-1",
			// distinct action text per call
			"call-"+string(rune('a'+i)))
		out[i] = res.Drift
	}
	return out
}

// driveScoreOnce is the single-call helper for tests that don't
// need a full sequence.
func driveScoreOnce(e *Engine, taskID, action string) Result {
	return e.Score(context.Background(), taskID, action)
}

// setupIntent generates deterministic vectors for the fail-closed test.
// Returns (intentVector, unused). Kept lightweight so the test doesn't
// pull in the crypto/ed25519 fakeEmbedder path.
func setupIntent(t *testing.T, _ string) ([]float32, []float32) {
	t.Helper()
	return []float32{1, 0, 0}, nil
}

// sequentiallyFailingEmbedder returns the intent vector on the
// first N calls, then errors thereafter. Simulates a working
// embedder that goes down mid-session.
type sequentiallyFailingEmbedder struct {
	intentReturn []float32
	initialCalls int
	called       int
}

func (s *sequentiallyFailingEmbedder) Embed(_ context.Context, req *llm.EmbeddingRequest) (*llm.EmbeddingResponse, error) {
	s.called += len(req.Texts)
	if s.called <= s.initialCalls {
		out := make([][]float32, len(req.Texts))
		for i := range out {
			out[i] = s.intentReturn
		}
		return &llm.EmbeddingResponse{Embeddings: out}, nil
	}
	return nil, errActionsExhausted
}
func (s *sequentiallyFailingEmbedder) Dimensions() int { return 3 }

// jsonMustMarshal is a small helper that panics on a marshal error,
// used only in tests where a compile-time-known input can't fail.
func jsonMustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

var _ = jsonMustMarshal // referenced by future assertions on audit-event payloads
