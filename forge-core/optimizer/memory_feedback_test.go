package optimizer

import (
	"path/filepath"
	"testing"
)

func TestEffectiveConfidence_MovesWithEvidence(t *testing.T) {
	base := 0.6
	if got := EffectiveConfidence(base, FeedbackStat{}); got < 0.59 || got > 0.61 {
		t.Errorf("no feedback should equal prior, got %.3f", got)
	}
	down := EffectiveConfidence(base, FeedbackStat{Neg: 3})
	up := EffectiveConfidence(base, FeedbackStat{Pos: 3})
	if !(down < base && up > base) {
		t.Errorf("expected down(%.3f) < %.2f < up(%.3f)", down, base, up)
	}
	if down >= 0.5 {
		t.Errorf("a strong downvote should drop below the 0.5 gate, got %.3f", down)
	}
}

func TestApplyFeedback_CreditsRecalledAndCorroborates(t *testing.T) {
	store, err := NewFileMemoryStore(filepath.Join(t.TempDir(), "mem.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// A procedure and an episode, both touching client.go, both recalled into S.
	_ = store.WriteEpisode(Episode{ID: "P", Kind: KindProcedural, Repo: "forge", Entities: []string{"client.go"}})
	_ = store.WriteEpisode(Episode{ID: "M", Kind: KindEpisodic, Repo: "forge", SessionID: "old", Entities: []string{"client.go"}})
	_ = store.RecordRecall("S", []string{"P", "M"})

	former := NewMemoryFormer(MemoryFormerConfig{Store: store, Distiller: &fakeDistiller{result: &Episode{}}, Repo: "forge"})
	// A new successful episode in S touching client.go.
	former.applyFeedback(Episode{ID: "E", Repo: "forge", SessionID: "S", Entities: []string{"client.go"}, Outcome: OutcomeSuccess})

	agg, err := store.FeedbackAgg()
	if err != nil {
		t.Fatal(err)
	}
	// Procedure: corroborate (1) + recall_success (0.5) = 1.5 positive.
	if agg["P"].Pos < 1.4 {
		t.Errorf("procedure P positive = %.2f, want ~1.5", agg["P"].Pos)
	}
	// Episode M: recall_success only (0.5).
	if agg["M"].Pos < 0.4 || agg["M"].Pos > 0.6 {
		t.Errorf("episode M positive = %.2f, want ~0.5", agg["M"].Pos)
	}
}

func TestRecall_ConfidenceGate_ExcludesLowConfidence(t *testing.T) {
	store, err := NewFileMemoryStore(filepath.Join(t.TempDir(), "mem.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	_ = store.WriteEpisode(Episode{
		ID: "x", Kind: KindEpisodic, Repo: "forge", SessionID: "old",
		TaskSignature: "flaky memory", Lesson: "maybe", Confidence: 0.6,
	})
	former := NewMemoryFormer(MemoryFormerConfig{
		Store: store, Distiller: &fakeDistiller{result: &Episode{}}, Repo: "forge",
		Recall: RecallConfig{Enabled: true, MinConfidence: 0.5},
	})
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)

	// Before feedback: injected (prior 0.6 ≥ 0.5).
	if _, n := former.Inject("s1", body); n != 1 {
		t.Fatalf("expected 1 injected before feedback, got %d", n)
	}

	// Two strong downvotes → effective confidence below the gate.
	_ = store.RecordFeedback("x", SignalDown, HumanFeedbackWeight, false)
	_ = store.RecordFeedback("x", SignalDown, HumanFeedbackWeight, false)

	// New session (recall is frozen per session, so use a fresh id).
	if _, n := former.Inject("s2", body); n != 0 {
		t.Fatalf("expected 0 injected after downvotes (gated out), got %d", n)
	}
}
