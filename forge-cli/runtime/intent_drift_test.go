package runtime

import (
	"testing"

	"github.com/initializ/forge/forge-core/types"
)

// TestIntentDriftConfig_ThresholdZeroPreserved pins the fix for the
// same zero-value-as-unset bug that Manoj flagged on #245, applied
// to R7's `drift_threshold`. Explicit `drift_threshold: 0` used to
// be silently overridden to 0.35 by `buildIntentEngine`, so an
// operator who wanted "flag drift only when the mean goes negative"
// couldn't express that.
func TestIntentDriftConfig_ThresholdZeroPreserved(t *testing.T) {
	yamlSrc := `
agent_id: intent-drift-zero-threshold
version: 0.1.0
framework: forge
entrypoint: main.py
security:
  intent_alignment:
    enabled: true
    provider: openai
    model: text-embedding-3-small
  intent_drift:
    enabled: true
    window: 5
    drift_threshold: 0
    monotone_n: 3
`
	cfg, err := types.ParseForgeConfig([]byte(yamlSrc))
	if err != nil {
		t.Fatalf("ParseForgeConfig: %v", err)
	}
	id := cfg.Security.IntentDrift
	if id.DriftThreshold == nil {
		t.Fatal("drift_threshold: 0 must materialize as a non-nil *float64 — pointer-zero collapsed to nil")
	}
	if *id.DriftThreshold != 0 {
		t.Errorf("drift_threshold: got %f, want 0", *id.DriftThreshold)
	}

	// Simulate the runner's defaulting path — same nil-check logic
	// as buildIntentEngine — and confirm the explicit 0 survives.
	driftThreshold := 0.35
	if id.DriftThreshold != nil {
		driftThreshold = *id.DriftThreshold
	}
	if driftThreshold != 0 {
		t.Errorf("runner defaulting collapsed explicit 0: got %f", driftThreshold)
	}
}

// TestIntentDriftConfig_ThresholdUnsetGetsDefault pins the other
// half: nil pointer means "unset, apply 0.35 default."
func TestIntentDriftConfig_ThresholdUnsetGetsDefault(t *testing.T) {
	yamlSrc := `
agent_id: intent-drift-defaults
version: 0.1.0
framework: forge
entrypoint: main.py
security:
  intent_alignment:
    enabled: true
    provider: openai
    model: text-embedding-3-small
  intent_drift:
    enabled: true
    window: 5
`
	cfg, err := types.ParseForgeConfig([]byte(yamlSrc))
	if err != nil {
		t.Fatalf("ParseForgeConfig: %v", err)
	}
	if cfg.Security.IntentDrift.DriftThreshold != nil {
		t.Errorf("drift_threshold should be nil when omitted, got %v",
			cfg.Security.IntentDrift.DriftThreshold)
	}
}
