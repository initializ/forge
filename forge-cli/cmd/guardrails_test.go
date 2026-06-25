package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/initializ/guardrails/models"
	"go.mongodb.org/mongo-driver/bson"
)

// TestGuardrailsSeedDefaults_RoundTripsThroughLibraryModel is the
// canary the issue calls out: pipe `forge guardrails seed-defaults`
// into a JSON consumer, and the JSON MUST unmarshal into the
// library's models.StructuredGuardrails. If we ever drift from the
// library schema this test fails in red. Verification step 4 of #166.
func TestGuardrailsSeedDefaults_RoundTripsThroughLibraryModel(t *testing.T) {
	var buf bytes.Buffer
	cmd := guardrailsSeedDefaultsCmd
	cmd.SetOut(&buf)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("seed-defaults: %v", err)
	}
	out := buf.String()
	// Library JSON tags are camelCase (customRules, jailbreakDetection,
	// promptInjection, …). The round-trip assertion below is the
	// strict invariant; this substring check is just a fast canary
	// against a totally-empty marshal.
	if !strings.Contains(out, "customRules") {
		t.Errorf("seed-defaults output missing customRules section; got:\n%s", out)
	}

	var sg models.StructuredGuardrails
	if err := json.Unmarshal(buf.Bytes(), &sg); err != nil {
		t.Fatalf("seed-defaults output does not round-trip through models.StructuredGuardrails: %v\noutput:\n%s",
			err, out)
	}
	// Sanity: the defaults must carry the canonical baseline. Below
	// 5 secret-pattern rules would mean the upstream defaults
	// regressed and the issue #166 baseline check would break too.
	if sg.CustomRules == nil || len(sg.CustomRules.Rules) < 5 {
		t.Errorf("seed-defaults dropped below baseline rule count; got %d", func() int {
			if sg.CustomRules == nil {
				return 0
			}
			return len(sg.CustomRules.Rules)
		}())
	}
	if sg.PII == nil || !sg.PII.Enabled {
		t.Errorf("seed-defaults missing or-disabled PII config: %+v", sg.PII)
	}
}

// TestScoreAgentConfig_FullDefaultsHaveNoWarnings is the inverse-side
// test: a doc constructed from DefaultStructuredGuardrails (via the
// seed-defaults JSON round-tripped through BSON) must produce zero
// warnings from scoreAgentConfig. If a future schema drift means the
// validate-db pass starts flagging the canonical seed, this test
// catches it before operators see false positives.
//
// Uses the camelCase key shape that matches the library struct tags
// (what production MongoDB documents actually carry). The
// snake_case fallback is exercised by TestScoreAgentConfig_SnakeCaseCompat
// below.
func TestScoreAgentConfig_FullDefaultsHaveNoWarnings(t *testing.T) {
	doc := bson.M{
		"customRules": bson.M{
			"rules": bson.A{
				bson.M{"id": "secret_anthropic", "name": "Anthropic API Key"},
				bson.M{"id": "secret_openai", "name": "OpenAI"},
				bson.M{"id": "secret_github_pat", "name": "GitHub PAT"},
				bson.M{"id": "secret_aws", "name": "AWS Access Key"},
				bson.M{"id": "secret_slack_bot", "name": "Slack Bot Token"},
				bson.M{"id": "secret_private_key", "name": "Private Key"},
			},
		},
		"pii": bson.M{"enabled": true},
		"security": bson.M{
			"jailbreakDetection": bson.M{"enabled": true},
			"promptInjection":    bson.M{"enabled": true},
			"commandInjection":   bson.M{"enabled": true},
		},
		"gateConfig": bson.M{
			"inputGate":    true,
			"outputGate":   true,
			"toolCallGate": true,
		},
	}
	r := scoreAgentConfig(doc)
	if len(r.warnings) != 0 {
		t.Errorf("full-defaults doc produced warnings: %v", r.warnings)
	}
}

// TestScoreAgentConfig_SnakeCaseCompat confirms the validator also
// accepts the snake_case spelling — legacy seeds and hand-written
// configs based on old docs use that convention. Same input as the
// camelCase case above, no warnings expected.
func TestScoreAgentConfig_SnakeCaseCompat(t *testing.T) {
	doc := bson.M{
		"custom_rules": bson.M{
			"rules": bson.A{
				bson.M{"id": "secret_a"}, bson.M{"id": "secret_b"},
				bson.M{"id": "secret_c"}, bson.M{"id": "secret_d"},
				bson.M{"id": "secret_e"},
			},
		},
		"pii": bson.M{"enabled": true},
		"security": bson.M{
			"jailbreak_detection": bson.M{"enabled": true},
			"prompt_injection":    bson.M{"enabled": true},
			"command_injection":   bson.M{"enabled": true},
		},
		"gate_config": bson.M{
			"input_gate":     true,
			"output_gate":    true,
			"tool_call_gate": true,
		},
	}
	r := scoreAgentConfig(doc)
	if len(r.warnings) != 0 {
		t.Errorf("snake_case-shaped doc produced warnings: %v", r.warnings)
	}
}

// TestScoreAgentConfig_EmptyDocFlagsEverything is the operator-error
// canary: an empty AgentConfig document (or one missing all the
// baseline blocks) must flag every coverage gap the issue documents.
// This is the most common deploy error — a developer creates an
// AgentConfig with just an agent_id field and assumes the library
// applies defaults. It doesn't.
func TestScoreAgentConfig_EmptyDocFlagsEverything(t *testing.T) {
	r := scoreAgentConfig(bson.M{})
	wantWarns := []string{
		"fewer than 5 secret-pattern rules",
		"no PII config",
		"no security config",
		"no gate_config",
	}
	for _, want := range wantWarns {
		found := false
		for _, w := range r.warnings {
			if strings.Contains(w, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected warning containing %q; got warnings=%v", want, r.warnings)
		}
	}
}

// TestScoreAgentConfig_FewerThan5SecretRulesWarns pins the explicit
// threshold the issue calls out: a doc with 4 secret-pattern rules
// (one below baseline) MUST warn about secret-pattern coverage, but
// nothing else if PII / security / gates are otherwise healthy.
func TestScoreAgentConfig_FewerThan5SecretRulesWarns(t *testing.T) {
	doc := bson.M{
		"customRules": bson.M{
			"rules": bson.A{
				bson.M{"id": "secret_one"},
				bson.M{"id": "secret_two"},
				bson.M{"id": "secret_three"},
				bson.M{"id": "secret_four"},
			},
		},
		"pii": bson.M{"enabled": true},
		"security": bson.M{
			"jailbreakDetection": bson.M{"enabled": true},
			"promptInjection":    bson.M{"enabled": true},
			"commandInjection":   bson.M{"enabled": true},
		},
		"gateConfig": bson.M{
			"inputGate":    true,
			"outputGate":   true,
			"toolCallGate": true,
		},
	}
	r := scoreAgentConfig(doc)
	if len(r.warnings) != 1 {
		t.Errorf("expected exactly 1 warning (secret-pattern coverage); got %d: %v",
			len(r.warnings), r.warnings)
	}
	if len(r.warnings) > 0 && !strings.Contains(r.warnings[0], "secret-pattern") {
		t.Errorf("warning should be about secret-pattern coverage; got %q", r.warnings[0])
	}
}

// TestExtractCustomRules_DefensiveOnShape walks the BSON-shape edge
// cases scoreAgentConfig must survive: missing custom_rules,
// custom_rules present but rules array wrong type, individual rule
// entries missing id, etc. The function MUST never panic and MUST
// fall back to an empty result so the warning surfaces correctly.
func TestExtractCustomRules_DefensiveOnShape(t *testing.T) {
	cases := []struct {
		name string
		doc  bson.M
		want int
	}{
		{"absent customRules", bson.M{}, 0},
		{"customRules not a map", bson.M{"customRules": "string"}, 0},
		{"rules not an array", bson.M{"customRules": bson.M{"rules": "oops"}}, 0},
		{"rule missing id", bson.M{"customRules": bson.M{"rules": bson.A{bson.M{}}}}, 0},
		{"rule has id camelCase", bson.M{"customRules": bson.M{"rules": bson.A{bson.M{"id": "secret_x"}}}}, 1},
		{"rule has id snake_case fallback", bson.M{"custom_rules": bson.M{"rules": bson.A{bson.M{"id": "secret_x"}}}}, 1},
		{"rule has name fallback", bson.M{"customRules": bson.M{"rules": bson.A{bson.M{"name": "X"}}}}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractCustomRules(c.doc)
			if len(got) != c.want {
				t.Errorf("extractCustomRules(%+v) = %v, want length %d", c.doc, got, c.want)
			}
		})
	}
}
