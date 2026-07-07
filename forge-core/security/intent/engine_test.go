package intent

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/initializ/forge/forge-core/llm"
)

// fakeEmbedder maps text → deterministic vector. The mapping is
// contrived to give the tests predictable cosine outcomes: texts
// starting with "READ" produce one vector family, texts starting
// with "WRITE" another (near-orthogonal), so we can build known-
// good and known-bad intent/action pairs without an actual LLM.
type fakeEmbedder struct {
	calls atomic.Int64
	err   error
}

func (f *fakeEmbedder) Embed(_ context.Context, req *llm.EmbeddingRequest) (*llm.EmbeddingResponse, error) {
	f.calls.Add(int64(len(req.Texts)))
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, len(req.Texts))
	for i, t := range req.Texts {
		out[i] = fakeVector(t)
	}
	return &llm.EmbeddingResponse{Embeddings: out, Model: "fake"}, nil
}

func (f *fakeEmbedder) Dimensions() int { return 3 }

// fakeVector picks one of three orthogonal-ish directions based on
// the first characters. Same prefix → identical vector → cosine=1.0.
// Different prefix → orthogonal vectors → cosine≈0.
func fakeVector(s string) []float32 {
	switch {
	case len(s) >= 4 && s[:4] == "READ":
		return []float32{1, 0, 0}
	case len(s) >= 5 && s[:5] == "WRITE":
		return []float32{0, 1, 0}
	case len(s) >= 6 && s[:6] == "DELETE":
		return []float32{0, 0, 1}
	default:
		// Anything else: a mixed vector so we get partial similarity.
		return []float32{0.577, 0.577, 0.577}
	}
}

func TestConfig_Validate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"disabled config is valid", Config{}, false},
		{"threshold out of range", Config{Enabled: true, Threshold: 1.5, HardThreshold: 0.3}, true},
		{"hard > soft rejected", Config{Enabled: true, Threshold: 0.3, HardThreshold: 0.5}, true},
		{"equal thresholds ok (disables warn tier)", Config{Enabled: true, Threshold: 0.5, HardThreshold: 0.5}, false},
		{"sensible defaults", Config{Enabled: true, Threshold: 0.5, HardThreshold: 0.3}, false},
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

func TestNew_EnabledWithoutEmbedderRejected(t *testing.T) {
	_, err := New(Config{Enabled: true, Threshold: 0.5, HardThreshold: 0.3}, nil)
	if err == nil {
		t.Fatal("expected error when enabled without embedder")
	}
}

// TestScore_KnownGoodPairAllows is the happy-path acceptance test:
// stated intent "READ S3 bucket X" pairs with an action to READ a
// bucket → cosine 1.0 → DecisionAllow.
func TestScore_KnownGoodPairAllows(t *testing.T) {
	e, err := New(Config{Enabled: true, Threshold: 0.5, HardThreshold: 0.3}, &fakeEmbedder{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := e.RegisterIntent(ctx, "task-1", "READ the S3 bucket foo"); err != nil {
		t.Fatalf("RegisterIntent: %v", err)
	}
	res := e.Score(ctx, "task-1", "READ file contents from S3")
	if res.Decision != DecisionAllow {
		t.Errorf("decision: got %s want allow (score=%.3f reason=%s)", res.Decision, res.Score, res.Reason)
	}
	if res.Score < 0.99 {
		t.Errorf("score too low for identical vectors: %.3f", res.Score)
	}
}

// TestScore_KnownBadPairDenies is the safety acceptance test: intent
// says READ, action tries DELETE → orthogonal vectors → cosine ~0
// → DecisionDeny (below hard_threshold 0.3).
func TestScore_KnownBadPairDenies(t *testing.T) {
	e, err := New(Config{Enabled: true, Threshold: 0.5, HardThreshold: 0.3}, &fakeEmbedder{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := e.RegisterIntent(ctx, "task-1", "READ the S3 bucket foo"); err != nil {
		t.Fatalf("RegisterIntent: %v", err)
	}
	res := e.Score(ctx, "task-1", "DELETE all objects in the S3 bucket")
	if res.Decision != DecisionDeny {
		t.Errorf("decision: got %s want deny (score=%.3f)", res.Decision, res.Score)
	}
	if res.Score > 0.1 {
		t.Errorf("orthogonal vectors should cosine near 0, got %.3f", res.Score)
	}
}

// TestScore_MidRangeScoreWarns exercises the warn tier: partial-
// alignment vector (mixed direction) → cosine ~0.577 → falls between
// hard (0.3) and soft (0.7) → DecisionWarn.
func TestScore_MidRangeScoreWarns(t *testing.T) {
	e, err := New(Config{Enabled: true, Threshold: 0.7, HardThreshold: 0.3}, &fakeEmbedder{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := e.RegisterIntent(ctx, "task-1", "READ the S3 bucket foo"); err != nil {
		t.Fatalf("RegisterIntent: %v", err)
	}
	// Action doesn't start with any known prefix → mixed vector →
	// cosine against pure-READ ≈ 0.577.
	res := e.Score(ctx, "task-1", "list objects generically")
	if res.Decision != DecisionWarn {
		t.Errorf("decision: got %s want warn (score=%.3f)", res.Decision, res.Score)
	}
}

// TestScore_FailClosedOnEmbedderError proves the governance-critical
// fail-closed posture: the intent was registered fine, but the
// action-side embedder call fails at Score time → DecisionDeny.
func TestScore_FailClosedOnEmbedderError(t *testing.T) {
	emb := &fakeEmbedder{}
	e, _ := New(Config{Enabled: true, Threshold: 0.5, HardThreshold: 0.3}, emb)
	ctx := context.Background()
	if err := e.RegisterIntent(ctx, "task-1", "READ the S3 bucket foo"); err != nil {
		t.Fatalf("RegisterIntent: %v", err)
	}
	emb.err = errors.New("simulated 429 from embedder")

	res := e.Score(ctx, "task-1", "READ another bucket")
	if res.Decision != DecisionDeny {
		t.Errorf("fail-closed: got %s want deny", res.Decision)
	}
	if !math.IsNaN(res.Score) {
		t.Errorf("expected NaN score on error path, got %.3f", res.Score)
	}
}

// TestScore_UnknownTaskFailsClosed guards a subtle attack surface:
// if a hook fires for a task the A2A handler didn't register (e.g.
// scheduled task with no user message), the engine denies rather
// than defaulting to a score against a zero vector.
func TestScore_UnknownTaskFailsClosed(t *testing.T) {
	e, _ := New(Config{Enabled: true, Threshold: 0.5, HardThreshold: 0.3}, &fakeEmbedder{})
	res := e.Score(context.Background(), "unknown-task", "READ anything")
	if res.Decision != DecisionDeny {
		t.Errorf("unknown task: got %s want deny", res.Decision)
	}
}

// TestScore_DisabledEngineAllowsWithoutEmbedderCall confirms the
// opt-in default is a true no-op: no embedder call, no scoring.
func TestScore_DisabledEngineAllowsWithoutEmbedderCall(t *testing.T) {
	emb := &fakeEmbedder{}
	e, _ := New(Config{Enabled: false}, emb)
	res := e.Score(context.Background(), "any-task", "any action")
	if res.Decision != DecisionAllow {
		t.Errorf("disabled: got %s want allow", res.Decision)
	}
	if emb.calls.Load() != 0 {
		t.Errorf("disabled engine called embedder %d times", emb.calls.Load())
	}
}

// TestRegisterIntent_IdempotentPerTask verifies the first intent
// sticks — a mid-conversation user message doesn't overwrite the
// original stated intent (the R3 spec says the FIRST user message
// on tasks/send).
func TestRegisterIntent_IdempotentPerTask(t *testing.T) {
	emb := &fakeEmbedder{}
	e, _ := New(Config{Enabled: true, Threshold: 0.5, HardThreshold: 0.3}, emb)
	ctx := context.Background()

	_ = e.RegisterIntent(ctx, "task-1", "READ the bucket")
	firstCalls := emb.calls.Load()
	_ = e.RegisterIntent(ctx, "task-1", "DELETE the bucket") // ignored
	if emb.calls.Load() != firstCalls {
		t.Errorf("second RegisterIntent triggered embedder: calls %d → %d", firstCalls, emb.calls.Load())
	}

	// Score still uses the FIRST intent (READ), so a DELETE action
	// should deny.
	res := e.Score(ctx, "task-1", "DELETE everything")
	if res.Decision != DecisionDeny {
		t.Errorf("second intent overrode first: got %s", res.Decision)
	}
}

// TestActionCache_HitsAvoidEmbedderCall confirms the LRU wins on
// repeated identical actions.
func TestActionCache_HitsAvoidEmbedderCall(t *testing.T) {
	emb := &fakeEmbedder{}
	e, _ := New(Config{Enabled: true, Threshold: 0.5, HardThreshold: 0.3, CacheSize: 8}, emb)
	ctx := context.Background()

	_ = e.RegisterIntent(ctx, "task-1", "READ the bucket")
	callsAfterRegister := emb.calls.Load()

	// First Score: cold action cache → embedder call.
	_ = e.Score(ctx, "task-1", "READ specific file")
	if emb.calls.Load()-callsAfterRegister != 1 {
		t.Errorf("first Score should trigger 1 action embed, got %d",
			emb.calls.Load()-callsAfterRegister)
	}

	// Second Score with same actionText: warm cache → NO embedder call.
	_ = e.Score(ctx, "task-1", "READ specific file")
	if emb.calls.Load()-callsAfterRegister != 1 {
		t.Errorf("second Score should hit cache, embedder called %d times total",
			emb.calls.Load()-callsAfterRegister)
	}
}

// TestForget_ClearsTaskState — after Forget, the task's intent is
// gone and a subsequent Score fails closed.
func TestForget_ClearsTaskState(t *testing.T) {
	e, _ := New(Config{Enabled: true, Threshold: 0.5, HardThreshold: 0.3}, &fakeEmbedder{})
	ctx := context.Background()
	_ = e.RegisterIntent(ctx, "task-1", "READ the bucket")
	e.Forget("task-1")
	res := e.Score(ctx, "task-1", "READ anything")
	if res.Decision != DecisionDeny {
		t.Errorf("after Forget: got %s want deny", res.Decision)
	}
}

// TestIntentTTL_Expired — an aged-out intent entry is treated as
// unknown, tripping the fail-closed path.
func TestIntentTTL_Expired(t *testing.T) {
	e, _ := New(Config{Enabled: true, Threshold: 0.5, HardThreshold: 0.3, IntentTTL: 1 * time.Millisecond},
		&fakeEmbedder{})
	// Use a fake clock so the test is deterministic.
	fakeNow := time.Now()
	e.nowFn = func() time.Time { return fakeNow }
	_ = e.RegisterIntent(context.Background(), "task-1", "READ the bucket")

	fakeNow = fakeNow.Add(1 * time.Hour)
	res := e.Score(context.Background(), "task-1", "READ anything")
	if res.Decision != DecisionDeny {
		t.Errorf("expired intent: got %s want deny", res.Decision)
	}
}

// TestCosine — pure numeric coverage of the similarity math.
func TestCosine(t *testing.T) {
	cases := []struct {
		name string
		a, b []float32
		want float64
	}{
		{"identical", []float32{1, 0, 0}, []float32{1, 0, 0}, 1.0},
		{"orthogonal", []float32{1, 0, 0}, []float32{0, 1, 0}, 0.0},
		{"opposite", []float32{1, 0, 0}, []float32{-1, 0, 0}, -1.0},
		{"scale-invariant", []float32{2, 0, 0}, []float32{1, 0, 0}, 1.0},
		{"zero-length invalid", []float32{}, []float32{1, 0}, 0.0},
		{"length mismatch invalid", []float32{1, 0}, []float32{1, 0, 0}, 0.0},
		{"zero vector invalid", []float32{0, 0, 0}, []float32{1, 0, 0}, 0.0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := cosine(c.a, c.b)
			if math.Abs(got-c.want) > 1e-6 {
				t.Errorf("got %.6f want %.6f", got, c.want)
			}
		})
	}
}

// TestLRU_EvictsLeastRecentlyUsed pins the LRU semantics — the
// action cache relies on this to bound memory under high tool-call
// diversity.
func TestLRU_EvictsLeastRecentlyUsed(t *testing.T) {
	c := newLRUCache(2)
	c.Put("a", []float32{1})
	c.Put("b", []float32{2})
	// Touch "a" so "b" is now oldest.
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a should be present")
	}
	c.Put("c", []float32{3})
	if _, ok := c.Get("b"); ok {
		t.Error("b should have been evicted as LRU")
	}
	if _, ok := c.Get("a"); !ok {
		t.Error("a should still be present (touched)")
	}
	if _, ok := c.Get("c"); !ok {
		t.Error("c should be present (just inserted)")
	}
}
