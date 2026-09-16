package optimizer

import "strings"

// Confidence dynamics. A memory's confidence is a Beta-style success rate seeded
// from its formation prior and moved by feedback events:
//
//	α = priorStrength·base + Σ positive weights
//	β = priorStrength·(1-base) + Σ negative weights
//	confidence = α / (α + β)
//
// Signals (see the wiring in memory_formation.go / handlers):
//   - up / down        — explicit human feedback (strong)
//   - corroborate      — a new episode reinforced this memory (shared entities)
//   - recall_success   — a session that used this memory then succeeded on a
//     related task (ambient, weaker)
//   - recall_failure   — same, but the related task failed
//
// Staleness decay is applied at recall time (memory_recall.go) when the repo has
// moved past the commit the memory was formed at.

// priorStrength is the pseudo-count weight of the formation prior. Higher =
// feedback moves confidence more slowly.
const priorStrength = 4.0

// Signal weights (magnitude applied to Pos/Neg).
const (
	HumanFeedbackWeight       = 3.0 // up / down
	feedbackWeightCorroborate = 1.0
	feedbackWeightRecall      = 0.5 // recall_success / recall_failure
)

// Signal names.
const (
	SignalUp            = "up"
	SignalDown          = "down"
	signalCorroborate   = "corroborate"
	signalRecallSuccess = "recall_success"
	signalRecallFailure = "recall_failure"
)

// EffectiveConfidence combines a base prior with accumulated feedback evidence.
func EffectiveConfidence(base float64, fb FeedbackStat) float64 {
	if base <= 0 {
		base = 0.6
	}
	alpha := priorStrength*base + fb.Pos
	beta := priorStrength*(1-base) + fb.Neg
	if alpha+beta <= 0 {
		return base
	}
	return alpha / (alpha + beta)
}

// applyFeedback runs the ambient feedback loop for a just-formed episode E:
//   - corroboration: procedures whose entities overlap E get a positive bump
//     (a fresh episode reinforced the generalized pattern);
//   - post-recall outcome: memories that were injected into E's session and
//     share entities with E are credited (E succeeded) or debited (E failed).
//
// Best-effort and off the request path (called from the dispatch goroutine).
func (f *MemoryFormer) applyFeedback(e Episode) {
	if f.store == nil {
		return
	}
	eEnts := entSet(entsOf(e))
	if len(eEnts) == 0 {
		return
	}

	all, err := f.store.ListEpisodes(e.Repo, 0)
	if err != nil {
		return
	}
	byID := make(map[string]Episode, len(all))
	for _, m := range all {
		byID[m.ID] = m
	}

	// Corroboration: reinforce overlapping procedures (skip self).
	for _, m := range all {
		if m.Kind != KindProcedural || m.ID == e.ID {
			continue
		}
		if overlaps(eEnts, entsOf(m)) {
			_ = f.store.RecordFeedback(m.ID, signalCorroborate, feedbackWeightCorroborate, true)
		}
	}

	// Post-recall outcome: attribute E's outcome to memories recalled into E's
	// session that are relevant (share entities). Neutral outcomes are ignored.
	if e.Outcome != OutcomeSuccess && e.Outcome != OutcomeFailure {
		return
	}
	recalled, err := f.store.RecalledInto(e.SessionID)
	if err != nil {
		return
	}
	positive := e.Outcome == OutcomeSuccess
	sig := signalRecallSuccess
	if !positive {
		sig = signalRecallFailure
	}
	for _, id := range recalled {
		if id == e.ID {
			continue
		}
		m, ok := byID[id]
		if !ok || !overlaps(eEnts, entsOf(m)) {
			continue
		}
		_ = f.store.RecordFeedback(id, sig, feedbackWeightRecall, positive)
	}
}

func entSet(items []string) map[string]struct{} {
	s := make(map[string]struct{}, len(items))
	for _, v := range items {
		if v = strings.TrimSpace(v); v != "" {
			s[v] = struct{}{}
		}
	}
	return s
}

func overlaps(set map[string]struct{}, items []string) bool {
	for _, v := range items {
		if _, ok := set[strings.TrimSpace(v)]; ok {
			return true
		}
	}
	return false
}
