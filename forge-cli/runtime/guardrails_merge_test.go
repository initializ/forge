package runtime

import (
	"reflect"
	"testing"

	"github.com/initializ/guardrails/models"
)

// TestMerge_EveryChangeIsRecorded pins review finding #3: whenever the
// overlay actually changes the effective config, at least one tightening is
// recorded — so the "tightened nothing" log can never be misleading. Covers
// the sections that were previously mutated silently (moderation, pii
// categories, nsfw threshold, urlFilter action, skillConstraints action).
func TestMerge_EveryChangeIsRecorded(t *testing.T) {
	cases := map[string]struct{ agent, platform *models.StructuredGuardrails }{
		"moderation.action": {
			agent:    &models.StructuredGuardrails{Moderation: &models.ModerationConfig{Enabled: true, Action: "warn"}},
			platform: &models.StructuredGuardrails{Moderation: &models.ModerationConfig{Enabled: true, Action: "block"}},
		},
		"pii.category": {
			agent: &models.StructuredGuardrails{PII: &models.PIIConfig{Enabled: true, Action: "mask",
				Categories: map[string]models.PIICategoryConfig{"email": {Enabled: false, Action: "warn"}}}},
			platform: &models.StructuredGuardrails{PII: &models.PIIConfig{Enabled: true, Action: "mask",
				Categories: map[string]models.PIICategoryConfig{"email": {Enabled: true, Action: "block"}}}},
		},
		"nsfw.threshold": {
			agent:    &models.StructuredGuardrails{NSFWText: &models.NSFWTextConfig{Enabled: true, ConfidenceThreshold: 0.8, Action: "block"}},
			platform: &models.StructuredGuardrails{NSFWText: &models.NSFWTextConfig{Enabled: true, ConfidenceThreshold: 0.3, Action: "block"}},
		},
		"urlFilter.action": {
			agent:    &models.StructuredGuardrails{URLFilter: &models.URLFilterConfig{Enabled: true, Action: "warn", Denylist: []string{"x.com"}}},
			platform: &models.StructuredGuardrails{URLFilter: &models.URLFilterConfig{Action: "block"}},
		},
		"skillConstraints.action": {
			agent:    &models.StructuredGuardrails{SkillConstraints: &models.SkillConstraintsConfig{Enabled: true, Action: "warn"}},
			platform: &models.StructuredGuardrails{SkillConstraints: &models.SkillConstraintsConfig{Action: "block"}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			effective, tt := MergeGuardrails(tc.agent, tc.platform)
			if reflect.DeepEqual(tc.agent, effective) {
				t.Skip("overlay produced no change for this case")
			}
			if len(tt) == 0 {
				t.Errorf("effective config changed but no tightening was recorded (misleading audit log)")
			}
		})
	}
}

func thr(enabled bool, threshold float64, action string) *models.ThresholdConfig {
	return &models.ThresholdConfig{Enabled: enabled, ConfidenceThreshold: threshold, Action: action}
}

// TestMerge_NilPlatform — a nil overlay returns a clone of the agent with no
// tightenings, and does not alias the input.
func TestMerge_NilPlatform(t *testing.T) {
	agent := &models.StructuredGuardrails{GateConfig: &models.GateConfig{OutputGate: true}}
	got, tt := MergeGuardrails(agent, nil)
	if len(tt) != 0 {
		t.Errorf("nil platform should tighten nothing, got %v", tt)
	}
	if got == agent {
		t.Error("result must be a copy, not the same pointer")
	}
	if got.GateConfig == agent.GateConfig {
		t.Error("nested GateConfig must be a copy, not aliased")
	}
}

// TestMerge_GateForceOn — platform can force a gate ON but never OFF.
func TestMerge_GateForceOn(t *testing.T) {
	agent := &models.StructuredGuardrails{GateConfig: &models.GateConfig{InputGate: true, OutputGate: false}}
	platform := &models.StructuredGuardrails{GateConfig: &models.GateConfig{OutputGate: true, InputGate: false}}
	got, tt := MergeGuardrails(agent, platform)
	if !got.GateConfig.OutputGate {
		t.Error("platform should force outputGate ON")
	}
	if !got.GateConfig.InputGate {
		t.Error("platform must NOT turn agent's inputGate OFF (never loosen)")
	}
	if len(tt) != 1 || tt[0].Field != "gateConfig.outputGate" {
		t.Errorf("expected one tightening on outputGate, got %v", tt)
	}
}

// TestMerge_ActionSeverity — most-severe action wins; a weaker platform
// action does not downgrade the agent's.
func TestMerge_ActionSeverity(t *testing.T) {
	agent := &models.StructuredGuardrails{Security: &models.SecurityConfig{CommandInjection: thr(true, 40, "warn")}}
	platform := &models.StructuredGuardrails{Security: &models.SecurityConfig{CommandInjection: thr(true, 40, "block")}}
	got, _ := MergeGuardrails(agent, platform)
	if got.Security.CommandInjection.Action != "block" {
		t.Errorf("expected action raised to block, got %q", got.Security.CommandInjection.Action)
	}

	// Reverse: platform weaker than agent must not loosen.
	agent2 := &models.StructuredGuardrails{Security: &models.SecurityConfig{CommandInjection: thr(true, 40, "block")}}
	platform2 := &models.StructuredGuardrails{Security: &models.SecurityConfig{CommandInjection: thr(true, 40, "warn")}}
	got2, tt2 := MergeGuardrails(agent2, platform2)
	if got2.Security.CommandInjection.Action != "block" {
		t.Errorf("weaker platform action must not loosen agent; got %q", got2.Security.CommandInjection.Action)
	}
	if len(tt2) != 0 {
		t.Errorf("no tightening expected when platform is weaker, got %v", tt2)
	}
}

// TestMerge_ThresholdStricter — lower (more sensitive) threshold wins; a
// higher platform threshold does not raise the agent's.
func TestMerge_ThresholdStricter(t *testing.T) {
	agent := &models.StructuredGuardrails{Security: &models.SecurityConfig{PromptInjection: thr(true, 50, "block")}}
	platform := &models.StructuredGuardrails{Security: &models.SecurityConfig{PromptInjection: thr(true, 30, "block")}}
	got, _ := MergeGuardrails(agent, platform)
	if got.Security.PromptInjection.ConfidenceThreshold != 30 {
		t.Errorf("expected threshold lowered to 30, got %g", got.Security.PromptInjection.ConfidenceThreshold)
	}

	platform2 := &models.StructuredGuardrails{Security: &models.SecurityConfig{PromptInjection: thr(true, 80, "block")}}
	got2, _ := MergeGuardrails(agent, platform2)
	if got2.Security.PromptInjection.ConfidenceThreshold != 50 {
		t.Errorf("higher platform threshold must not loosen; expected 50, got %g", got2.Security.PromptInjection.ConfidenceThreshold)
	}
}

// TestMerge_DetectionEnable — platform force-enables a detection the agent
// left off, and can add one the agent omitted entirely.
func TestMerge_DetectionEnable(t *testing.T) {
	agent := &models.StructuredGuardrails{Security: &models.SecurityConfig{CommandInjection: thr(false, 40, "warn")}}
	platform := &models.StructuredGuardrails{Security: &models.SecurityConfig{
		CommandInjection: thr(true, 40, "warn"),
		SQLInjection:     thr(true, 20, "block"), // agent had none
	}}
	got, _ := MergeGuardrails(agent, platform)
	if !got.Security.CommandInjection.Enabled {
		t.Error("platform should force commandInjection enabled")
	}
	if got.Security.SQLInjection == nil || !got.Security.SQLInjection.Enabled {
		t.Error("platform should add the sqlInjection detector the agent omitted")
	}
}

// TestMerge_CustomRulesUnion — platform rules are added; duplicates by ID
// are not; agent rules are never removed.
func TestMerge_CustomRulesUnion(t *testing.T) {
	agent := &models.StructuredGuardrails{CustomRules: &models.CustomRulesConfig{
		Rules: []models.CustomRule{{ID: "a", Action: "mask"}},
	}}
	platform := &models.StructuredGuardrails{CustomRules: &models.CustomRulesConfig{
		Rules: []models.CustomRule{{ID: "a", Action: "block"}, {ID: "b", Action: "block"}},
	}}
	got, _ := MergeGuardrails(agent, platform)
	if len(got.CustomRules.Rules) != 2 {
		t.Fatalf("expected union of 2 rules (dedupe id 'a'), got %d", len(got.CustomRules.Rules))
	}
	if got.CustomRules.Rules[0].ID != "a" || got.CustomRules.Rules[0].Action != "mask" {
		t.Error("agent's rule 'a' must be preserved unchanged (union does not overwrite)")
	}
}

// TestMerge_URLFilter — denylist unions, allowlist intersects.
func TestMerge_URLFilter(t *testing.T) {
	agent := &models.StructuredGuardrails{URLFilter: &models.URLFilterConfig{
		Enabled: true, Allowlist: []string{"a.com", "b.com"}, Denylist: []string{"x.com"},
	}}
	platform := &models.StructuredGuardrails{URLFilter: &models.URLFilterConfig{
		Allowlist: []string{"b.com", "c.com"}, Denylist: []string{"y.com"},
	}}
	got, _ := MergeGuardrails(agent, platform)
	if len(got.URLFilter.Denylist) != 2 {
		t.Errorf("denylist should union to 2, got %v", got.URLFilter.Denylist)
	}
	if len(got.URLFilter.Allowlist) != 1 || got.URLFilter.Allowlist[0] != "b.com" {
		t.Errorf("allowlist should intersect to [b.com], got %v", got.URLFilter.Allowlist)
	}
}

// TestMerge_SkillConstraints — blocked unions, allowed intersects.
func TestMerge_SkillConstraints(t *testing.T) {
	agent := &models.StructuredGuardrails{SkillConstraints: &models.SkillConstraintsConfig{
		Enabled: true, AllowedSkills: []string{"s1", "s2"}, BlockedSkills: []string{"bad1"},
	}}
	platform := &models.StructuredGuardrails{SkillConstraints: &models.SkillConstraintsConfig{
		AllowedSkills: []string{"s2", "s3"}, BlockedSkills: []string{"bad2"},
	}}
	got, _ := MergeGuardrails(agent, platform)
	if len(got.SkillConstraints.BlockedSkills) != 2 {
		t.Errorf("blocked should union to 2, got %v", got.SkillConstraints.BlockedSkills)
	}
	if len(got.SkillConstraints.AllowedSkills) != 1 || got.SkillConstraints.AllowedSkills[0] != "s2" {
		t.Errorf("allowed should intersect to [s2], got %v", got.SkillConstraints.AllowedSkills)
	}
}

// TestMerge_NeverLoosen_InputUnmutated — a fully-weaker overlay changes
// nothing, and the agent input is never mutated by the merge.
func TestMerge_NeverLoosen_InputUnmutated(t *testing.T) {
	agent := &models.StructuredGuardrails{
		GateConfig: &models.GateConfig{InputGate: true, OutputGate: true, ToolCallGate: true},
		Security:   &models.SecurityConfig{CommandInjection: thr(true, 20, "block")},
		PII:        &models.PIIConfig{Enabled: true, Action: "block"},
	}
	platform := &models.StructuredGuardrails{
		GateConfig: &models.GateConfig{}, // all false
		Security:   &models.SecurityConfig{CommandInjection: thr(false, 90, "warn")},
		PII:        &models.PIIConfig{Enabled: false, Action: "warn"},
	}
	got, tt := MergeGuardrails(agent, platform)
	if len(tt) != 0 {
		t.Errorf("weaker overlay must tighten nothing, got %v", tt)
	}
	// Agent still strict.
	if !got.GateConfig.OutputGate || got.Security.CommandInjection.Action != "block" || got.PII.Action != "block" {
		t.Error("agent settings must be preserved against a weaker overlay")
	}
	// Input untouched.
	if agent.PII.Action != "block" || agent.Security.CommandInjection.ConfidenceThreshold != 20 {
		t.Error("MergeGuardrails must not mutate the agent input")
	}
}
