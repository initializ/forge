package analyzer

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-skills/contract"
)

func TestGenerateReportFromEntries(t *testing.T) {
	entries := []contract.SkillEntry{
		{
			Name: "github",
			ForgeReqs: &contract.SkillRequirements{
				Bins: []contract.BinRequirement{{Name: "gh"}},
				Env:  &contract.EnvRequirements{Required: []string{"GH_TOKEN"}},
			},
			Metadata: &contract.SkillMetadata{
				Metadata: map[string]map[string]any{
					"forge": {
						"egress_domains": []any{"api.github.com", "github.com"},
					},
				},
			},
		},
		{
			Name: "simple",
		},
	}

	hasScript := func(name string) bool { return false }
	report := GenerateReportFromEntries(entries, hasScript, DefaultPolicy())

	if report.SkillCount != 2 {
		t.Fatalf("expected 2 skills, got %d", report.SkillCount)
	}
	if len(report.Assessments) != 2 {
		t.Fatalf("expected 2 assessments, got %d", len(report.Assessments))
	}
	if report.Assessments[0].SkillName != "github" {
		t.Fatalf("expected first skill 'github', got %q", report.Assessments[0].SkillName)
	}
}

func TestGenerateReportFromEntries_PolicyFail(t *testing.T) {
	entries := []contract.SkillEntry{
		{
			Name: "hacker",
			ForgeReqs: &contract.SkillRequirements{
				Bins: []contract.BinRequirement{{Name: "nc"}},
			},
		},
	}

	hasScript := func(name string) bool { return false }
	report := GenerateReportFromEntries(entries, hasScript, DefaultPolicy())

	if report.PolicySummary.Passed {
		t.Fatal("expected policy to fail")
	}
	if report.PolicySummary.Errors == 0 {
		t.Fatal("expected errors > 0")
	}
}

func TestFormatJSON(t *testing.T) {
	entries := []contract.SkillEntry{{Name: "test"}}
	report := GenerateReportFromEntries(entries, nil, DefaultPolicy())

	data, err := FormatJSON(report)
	if err != nil {
		t.Fatalf("FormatJSON failed: %v", err)
	}

	// Verify it's valid JSON
	var parsed AuditReport
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if parsed.SkillCount != 1 {
		t.Fatalf("expected skill_count 1, got %d", parsed.SkillCount)
	}
}

func TestFormatText(t *testing.T) {
	entries := []contract.SkillEntry{
		{
			Name: "github",
			ForgeReqs: &contract.SkillRequirements{
				Bins: []contract.BinRequirement{{Name: "gh"}},
				Env:  &contract.EnvRequirements{Required: []string{"GH_TOKEN"}},
			},
			Metadata: &contract.SkillMetadata{
				Metadata: map[string]map[string]any{
					"forge": {
						"egress_domains": []any{"api.github.com"},
					},
				},
			},
		},
	}

	hasScript := func(name string) bool { return false }
	report := GenerateReportFromEntries(entries, hasScript, DefaultPolicy())
	text := FormatText(report)

	if !strings.Contains(text, "Security Audit Report") {
		t.Fatal("missing header")
	}
	if !strings.Contains(text, "github") {
		t.Fatal("missing skill name")
	}
	if !strings.Contains(text, "Aggregate Risk") {
		t.Fatal("missing aggregate risk")
	}
	if !strings.Contains(text, "Policy Summary") {
		t.Fatal("missing policy summary")
	}
}

func TestFormatText_WithViolations(t *testing.T) {
	entries := []contract.SkillEntry{
		{
			Name: "scripted",
		},
	}

	hasScript := func(name string) bool { return name == "scripted" }
	report := GenerateReportFromEntries(entries, hasScript, DefaultPolicy())
	text := FormatText(report)

	if !strings.Contains(text, "WARN") {
		t.Fatal("expected WARN in output")
	}
}

func TestAggregateScore_Average(t *testing.T) {
	entries := []contract.SkillEntry{
		{Name: "a"},
		{Name: "b", ForgeReqs: &contract.SkillRequirements{
			Bins: []contract.BinRequirement{{Name: "bash"}}, // 15 points
		}},
	}

	report := GenerateReportFromEntries(entries, nil, DefaultPolicy())

	// Expected: (0 + 15) / 2 = 7
	if report.AggregateScore.Value != 7 {
		t.Fatalf("expected aggregate 7, got %d", report.AggregateScore.Value)
	}
}

// TestReport_CriticalViolationFailsReport pins the load-bearing enforcement:
// a Critical policy violation must fold into the error count so PolicySummary
// reports the report as failed. This is what makes the browser +
// trust_hints.network:false trust conflict actually block a build/audit.
func TestReport_CriticalViolationFailsReport(t *testing.T) {
	netFalse := false
	entries := []contract.SkillEntry{
		{
			Name: "sneaky",
			Metadata: &contract.SkillMetadata{
				Name: "sneaky",
				Metadata: map[string]map[string]any{"forge": {
					"requires":    map[string]any{"capabilities": []any{"browser"}},
					"trust_hints": map[string]any{"network": netFalse},
					"guardrails":  map[string]any{"deny_output": []any{map[string]any{"pattern": "x", "action": "redact"}}},
				}},
			},
			ForgeReqs: &contract.SkillRequirements{Capabilities: []string{"browser"}},
		},
	}
	report := GenerateReportFromEntries(entries, func(string) bool { return false }, DefaultPolicy())

	if report.PolicySummary.Passed {
		t.Error("report passed despite a Critical trust-conflict violation")
	}
	if report.PolicySummary.Errors < 1 {
		t.Errorf("Errors = %d, want the Critical violation counted as an error", report.PolicySummary.Errors)
	}
	// Confirm the specific violation is present.
	found := false
	for _, a := range report.Assessments {
		for _, v := range a.Violations {
			if v.Rule == "capability_trust_conflict" && v.Severity == "critical" {
				found = true
			}
		}
	}
	if !found {
		t.Error("critical capability_trust_conflict violation not present in report")
	}
}
