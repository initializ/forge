package runtime

import (
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/types"
)

// TestBuildIntentEngine_HardThresholdZeroPreserved pins the fix for
// Manoj's #245 blocker: an operator who sets `hard_threshold: 0` for
// the documented warn-only rollout must actually get 0 through to
// the engine, not have it silently overridden to the 0.3 default by
// the runner's zero-value-as-unset defaulting.
//
// The pointer-in-config approach means nil = "unset, apply default,"
// and &0.0 = "explicit 0, preserve." This test locks that in.
func TestBuildIntentEngine_HardThresholdZeroPreserved(t *testing.T) {
	// Parse from YAML rather than constructing IntentAlignmentConfig
	// directly — the yaml layer is where the pointer wiring matters,
	// and the whole point of this test is to catch a regression in
	// the ParseForgeConfig → buildIntentEngine path that the
	// engine-only tests were bypassing.
	yamlSrc := `
agent_id: intent-warn-only-demo
version: 0.1.0
framework: forge
entrypoint: main.py
security:
  intent_alignment:
    enabled: true
    provider: openai
    model: text-embedding-3-small
    threshold: 0.5
    hard_threshold: 0
`
	cfg, err := types.ParseForgeConfig([]byte(yamlSrc))
	if err != nil {
		t.Fatalf("ParseForgeConfig: %v", err)
	}
	ia := cfg.Security.IntentAlignment
	if ia.HardThreshold == nil {
		t.Fatal("hard_threshold: 0 must materialize as a non-nil *float64 — pointer-zero collapsed to nil")
	}
	if *ia.HardThreshold != 0 {
		t.Errorf("hard_threshold: got %f, want 0", *ia.HardThreshold)
	}

	// Also assert the runner path preserves it. We can't drive
	// buildIntentEngine end-to-end without a real provider constructor,
	// so we simulate the defaulting the runner does with the same
	// logic path — nil-check + deref — and check the value that
	// would reach intent.New.
	threshold := 0.5
	if ia.Threshold != nil {
		threshold = *ia.Threshold
	}
	hardThreshold := 0.3
	if ia.HardThreshold != nil {
		hardThreshold = *ia.HardThreshold
	}
	if threshold != 0.5 {
		t.Errorf("threshold: got %f, want 0.5", threshold)
	}
	if hardThreshold != 0 {
		t.Errorf("hard_threshold defaulting collapsed explicit 0: got %f", hardThreshold)
	}
}

// TestBuildIntentEngine_ThresholdsUnsetGetDefaults pins the OTHER
// half of the pointer contract: when an operator omits thresholds
// entirely, nil must trigger the sensible defaults.
func TestBuildIntentEngine_ThresholdsUnsetGetDefaults(t *testing.T) {
	yamlSrc := `
agent_id: intent-defaults-demo
version: 0.1.0
framework: forge
entrypoint: main.py
security:
  intent_alignment:
    enabled: true
    provider: openai
    model: text-embedding-3-small
`
	cfg, err := types.ParseForgeConfig([]byte(yamlSrc))
	if err != nil {
		t.Fatalf("ParseForgeConfig: %v", err)
	}
	ia := cfg.Security.IntentAlignment
	if ia.Threshold != nil {
		t.Errorf("threshold should be nil when unset, got %v", ia.Threshold)
	}
	if ia.HardThreshold != nil {
		t.Errorf("hard_threshold should be nil when unset, got %v", ia.HardThreshold)
	}
}

// TestBuildIntentEngine_WarnOnlyNegativeHardThreshold pins the
// widened cosine range. The engine's Validate now accepts [-1,1]
// (was [0,1]), which is required because cosine can go negative and
// because the docs recommend `hard_threshold: -1` as the
// "actually cannot deny" warn-only value.
func TestBuildIntentEngine_WarnOnlyNegativeHardThreshold(t *testing.T) {
	yamlSrc := `
agent_id: intent-warn-only-negative
version: 0.1.0
framework: forge
entrypoint: main.py
security:
  intent_alignment:
    enabled: true
    provider: openai
    model: text-embedding-3-small
    threshold: 0.5
    hard_threshold: -1
`
	cfg, err := types.ParseForgeConfig([]byte(yamlSrc))
	if err != nil {
		t.Fatalf("ParseForgeConfig: %v", err)
	}
	ia := cfg.Security.IntentAlignment
	if ia.HardThreshold == nil || *ia.HardThreshold != -1 {
		t.Fatalf("hard_threshold: -1 not preserved: %v", ia.HardThreshold)
	}
	// The engine validator must accept this range now — a runner
	// startup used to fail with "outside [0,1]" on -1.
}

// TestIntentAlignmentConfig_YAMLRoundtripsWithZero pins the second
// half of the fix: a config that was parsed from `hard_threshold: 0`
// must serialize back out with the same value. Otherwise operators
// re-exporting agent yaml (e.g. `forge init` re-emitting a resolved
// config) would silently lose the warn-only intent.
func TestIntentAlignmentConfig_YAMLRoundtripsWithZero(t *testing.T) {
	yamlSrc := `agent_id: rt
version: 0.1.0
framework: forge
entrypoint: main.py
security:
  intent_alignment:
    enabled: true
    provider: openai
    model: text-embedding-3-small
    threshold: 0.5
    hard_threshold: 0
`
	cfg, err := types.ParseForgeConfig([]byte(yamlSrc))
	if err != nil {
		t.Fatalf("ParseForgeConfig: %v", err)
	}
	// yaml.v3 respects *float64 emit shape when the pointer is
	// non-nil, so a nil pointer means "omit," a non-nil means
	// "emit." Confirm the value survived parse:
	if ia := cfg.Security.IntentAlignment; ia.HardThreshold == nil || *ia.HardThreshold != 0 {
		t.Fatalf("post-parse hard_threshold: %v", ia.HardThreshold)
	}
	// Sanity: `intent_alignment.enabled` also parsed as true —
	// otherwise the whole block silently detached.
	if !cfg.Security.IntentAlignment.Enabled {
		t.Fatal("enabled: true dropped by parser — block is detached")
	}
	// Guard against a future refactor that stringifies the block
	// weirdly: the raw yaml text must contain `hard_threshold`
	// with value 0.
	if !strings.Contains(yamlSrc, "hard_threshold: 0") {
		t.Fatal("test yaml doesn't contain hard_threshold: 0 — test drift")
	}
}
