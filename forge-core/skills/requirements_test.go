package skills

import (
	"testing"
)

func TestAggregate_SingleSkill(t *testing.T) {
	entries := []SkillEntry{
		{
			Name: "summarize",
			ForgeReqs: &SkillRequirements{
				Bins: []string{"curl", "jq"},
				Env: &EnvRequirements{
					Required: []string{"API_KEY"},
					Optional: []string{"TIMEOUT"},
				},
			},
		},
	}

	reqs := AggregateRequirements(entries)
	if len(reqs.Bins) != 2 {
		t.Errorf("expected 2 bins, got %d", len(reqs.Bins))
	}
	if reqs.Bins[0] != "curl" || reqs.Bins[1] != "jq" {
		t.Errorf("bins = %v, want [curl jq]", reqs.Bins)
	}
	if len(reqs.EnvRequired) != 1 || reqs.EnvRequired[0] != "API_KEY" {
		t.Errorf("EnvRequired = %v, want [API_KEY]", reqs.EnvRequired)
	}
	if len(reqs.EnvOptional) != 1 || reqs.EnvOptional[0] != "TIMEOUT" {
		t.Errorf("EnvOptional = %v, want [TIMEOUT]", reqs.EnvOptional)
	}
}

func TestAggregate_MultiSkill_BinsUnion(t *testing.T) {
	entries := []SkillEntry{
		{
			Name:      "a",
			ForgeReqs: &SkillRequirements{Bins: []string{"curl", "jq"}},
		},
		{
			Name:      "b",
			ForgeReqs: &SkillRequirements{Bins: []string{"jq", "python"}},
		},
	}

	reqs := AggregateRequirements(entries)
	if len(reqs.Bins) != 3 {
		t.Errorf("expected 3 bins, got %d: %v", len(reqs.Bins), reqs.Bins)
	}
	// Should be sorted and deduplicated
	expected := []string{"curl", "jq", "python"}
	for i, b := range expected {
		if reqs.Bins[i] != b {
			t.Errorf("bins[%d] = %q, want %q", i, reqs.Bins[i], b)
		}
	}
}

func TestAggregate_PromotionOptionalToRequired(t *testing.T) {
	entries := []SkillEntry{
		{
			Name: "a",
			ForgeReqs: &SkillRequirements{
				Env: &EnvRequirements{
					Required: []string{"API_KEY"},
				},
			},
		},
		{
			Name: "b",
			ForgeReqs: &SkillRequirements{
				Env: &EnvRequirements{
					Optional: []string{"API_KEY", "DEBUG"},
				},
			},
		},
	}

	reqs := AggregateRequirements(entries)
	// API_KEY should be promoted to required (from optional in skill B)
	if len(reqs.EnvRequired) != 1 || reqs.EnvRequired[0] != "API_KEY" {
		t.Errorf("EnvRequired = %v, want [API_KEY]", reqs.EnvRequired)
	}
	// DEBUG should remain optional
	if len(reqs.EnvOptional) != 1 || reqs.EnvOptional[0] != "DEBUG" {
		t.Errorf("EnvOptional = %v, want [DEBUG]", reqs.EnvOptional)
	}
}

func TestAggregate_OneOfKeptSeparate(t *testing.T) {
	entries := []SkillEntry{
		{
			Name: "a",
			ForgeReqs: &SkillRequirements{
				Env: &EnvRequirements{
					OneOf: []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY"},
				},
			},
		},
		{
			Name: "b",
			ForgeReqs: &SkillRequirements{
				Env: &EnvRequirements{
					OneOf: []string{"GCP_KEY", "AWS_KEY"},
				},
			},
		},
	}

	reqs := AggregateRequirements(entries)
	if len(reqs.EnvOneOf) != 2 {
		t.Fatalf("expected 2 oneOf groups, got %d", len(reqs.EnvOneOf))
	}
	if len(reqs.EnvOneOf[0]) != 2 {
		t.Errorf("group 0 = %v, want 2 items", reqs.EnvOneOf[0])
	}
	if len(reqs.EnvOneOf[1]) != 2 {
		t.Errorf("group 1 = %v, want 2 items", reqs.EnvOneOf[1])
	}
}

func TestAggregate_NoRequirements(t *testing.T) {
	entries := []SkillEntry{
		{Name: "a"},
		{Name: "b"},
	}

	reqs := AggregateRequirements(entries)
	if len(reqs.Bins) != 0 {
		t.Errorf("expected 0 bins, got %d", len(reqs.Bins))
	}
	if len(reqs.EnvRequired) != 0 {
		t.Errorf("expected 0 required, got %d", len(reqs.EnvRequired))
	}
	if len(reqs.EnvOptional) != 0 {
		t.Errorf("expected 0 optional, got %d", len(reqs.EnvOptional))
	}
	if len(reqs.EnvOneOf) != 0 {
		t.Errorf("expected 0 oneOf, got %d", len(reqs.EnvOneOf))
	}
}
