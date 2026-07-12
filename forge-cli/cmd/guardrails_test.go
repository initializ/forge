package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/initializ/guardrails/models"
)

// TestGuardrailsSeedDefaults_RoundTripsThroughLibraryModel is the canary:
// pipe `forge guardrails seed-defaults` into a JSON consumer, and the JSON
// MUST unmarshal into the library's models.StructuredGuardrails. If we ever
// drift from the library schema this test fails in red.
func TestGuardrailsSeedDefaults_RoundTripsThroughLibraryModel(t *testing.T) {
	var buf bytes.Buffer
	cmd := guardrailsSeedDefaultsCmd
	cmd.SetOut(&buf)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("seed-defaults: %v", err)
	}
	out := buf.String()
	// Library JSON tags are camelCase (customRules, jailbreakDetection,
	// promptInjection, …). This substring check is a fast canary against a
	// totally-empty marshal; the round-trip below is the strict invariant.
	if !strings.Contains(out, "customRules") {
		t.Errorf("seed-defaults output missing customRules section; got:\n%s", out)
	}

	var sg models.StructuredGuardrails
	if err := json.Unmarshal(buf.Bytes(), &sg); err != nil {
		t.Fatalf("seed-defaults output does not round-trip through models.StructuredGuardrails: %v\noutput:\n%s",
			err, out)
	}
	// Sanity: the defaults must carry the canonical baseline.
	if sg.CustomRules == nil || len(sg.CustomRules.Rules) < 5 {
		n := 0
		if sg.CustomRules != nil {
			n = len(sg.CustomRules.Rules)
		}
		t.Errorf("seed-defaults dropped below baseline rule count; got %d", n)
	}
	if sg.PII == nil || !sg.PII.Enabled {
		t.Errorf("seed-defaults missing or-disabled PII config: %+v", sg.PII)
	}
}
