package requirements

import (
	"testing"

	"github.com/initializ/forge/forge-skills/contract"
)

func TestAggregate_SingleSkill(t *testing.T) {
	entries := []contract.SkillEntry{
		{
			Name: "github",
			ForgeReqs: &contract.SkillRequirements{
				Bins: []string{"curl", "jq"},
				Env: &contract.EnvRequirements{
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
	entries := []contract.SkillEntry{
		{
			Name:      "a",
			ForgeReqs: &contract.SkillRequirements{Bins: []string{"curl", "jq"}},
		},
		{
			Name:      "b",
			ForgeReqs: &contract.SkillRequirements{Bins: []string{"jq", "python"}},
		},
	}

	reqs := AggregateRequirements(entries)
	if len(reqs.Bins) != 3 {
		t.Errorf("expected 3 bins, got %d: %v", len(reqs.Bins), reqs.Bins)
	}
	expected := []string{"curl", "jq", "python"}
	for i, b := range expected {
		if reqs.Bins[i] != b {
			t.Errorf("bins[%d] = %q, want %q", i, reqs.Bins[i], b)
		}
	}
}

func TestAggregate_PromotionOptionalToRequired(t *testing.T) {
	entries := []contract.SkillEntry{
		{
			Name: "a",
			ForgeReqs: &contract.SkillRequirements{
				Env: &contract.EnvRequirements{
					Required: []string{"API_KEY"},
				},
			},
		},
		{
			Name: "b",
			ForgeReqs: &contract.SkillRequirements{
				Env: &contract.EnvRequirements{
					Optional: []string{"API_KEY", "DEBUG"},
				},
			},
		},
	}

	reqs := AggregateRequirements(entries)
	if len(reqs.EnvRequired) != 1 || reqs.EnvRequired[0] != "API_KEY" {
		t.Errorf("EnvRequired = %v, want [API_KEY]", reqs.EnvRequired)
	}
	if len(reqs.EnvOptional) != 1 || reqs.EnvOptional[0] != "DEBUG" {
		t.Errorf("EnvOptional = %v, want [DEBUG]", reqs.EnvOptional)
	}
}

func TestAggregate_OneOfKeptSeparate(t *testing.T) {
	entries := []contract.SkillEntry{
		{
			Name: "a",
			ForgeReqs: &contract.SkillRequirements{
				Env: &contract.EnvRequirements{
					OneOf: []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY"},
				},
			},
		},
		{
			Name: "b",
			ForgeReqs: &contract.SkillRequirements{
				Env: &contract.EnvRequirements{
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

func TestAggregate_DeniedToolsCollected(t *testing.T) {
	entries := []contract.SkillEntry{
		{
			Name: "k8s-triage",
			Metadata: &contract.SkillMetadata{
				Metadata: map[string]map[string]any{
					"forge": {
						"denied_tools": []any{"http_request", "web_search"},
					},
				},
			},
			ForgeReqs: &contract.SkillRequirements{
				Bins: []string{"kubectl"},
			},
		},
		{
			Name: "another-skill",
			Metadata: &contract.SkillMetadata{
				Metadata: map[string]map[string]any{
					"forge": {
						"denied_tools": []any{"web_search", "csv_parse"},
					},
				},
			},
		},
	}

	reqs := AggregateRequirements(entries)
	// Should be deduplicated and sorted: csv_parse, http_request, web_search
	if len(reqs.DeniedTools) != 3 {
		t.Fatalf("DeniedTools = %v, want 3 items", reqs.DeniedTools)
	}
	expected := []string{"csv_parse", "http_request", "web_search"}
	for i, v := range expected {
		if reqs.DeniedTools[i] != v {
			t.Errorf("DeniedTools[%d] = %q, want %q", i, reqs.DeniedTools[i], v)
		}
	}
}

func TestAggregate_NoRequirements(t *testing.T) {
	entries := []contract.SkillEntry{
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
