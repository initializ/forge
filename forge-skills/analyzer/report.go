package analyzer

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/initializ/forge/forge-skills/contract"
)

// GenerateReport produces a full audit report from a SkillRegistry.
func GenerateReport(registry contract.SkillRegistry, policy SecurityPolicy) (*AuditReport, error) {
	skills, err := registry.List()
	if err != nil {
		return nil, fmt.Errorf("listing skills: %w", err)
	}

	entries := make([]contract.SkillEntry, 0, len(skills))
	for _, sd := range skills {
		entry := contract.SkillEntry{
			Name:        sd.Name,
			Description: sd.Description,
		}
		if len(sd.RequiredBins) > 0 || len(sd.RequiredEnv) > 0 || len(sd.OneOfEnv) > 0 || len(sd.OptionalEnv) > 0 || len(sd.EgressDomains) > 0 {
			entry.ForgeReqs = &contract.SkillRequirements{
				Bins: sd.RequiredBins,
			}
			if len(sd.RequiredEnv) > 0 || len(sd.OneOfEnv) > 0 || len(sd.OptionalEnv) > 0 {
				entry.ForgeReqs.Env = &contract.EnvRequirements{
					Required: sd.RequiredEnv,
					OneOf:    sd.OneOfEnv,
					Optional: sd.OptionalEnv,
				}
			}
			if len(sd.EgressDomains) > 0 {
				entry.Metadata = &contract.SkillMetadata{
					Metadata: map[string]map[string]any{
						"forge": {
							"egress_domains": toAnySlice(sd.EgressDomains),
						},
					},
				}
			}
		}
		entries = append(entries, entry)
	}

	hasScript := func(name string) bool {
		return registry.HasScript(name)
	}

	return GenerateReportFromEntries(entries, hasScript, policy), nil
}

// GenerateReportFromEntries produces an audit report from parsed skill entries.
func GenerateReportFromEntries(entries []contract.SkillEntry, hasScript func(string) bool, policy SecurityPolicy) *AuditReport {
	report := &AuditReport{
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		SkillCount:  len(entries),
		Assessments: make([]SkillRiskAssessment, 0, len(entries)),
	}

	totalScore := 0
	totalErrors := 0
	totalWarnings := 0

	for i := range entries {
		entry := &entries[i]
		hs := hasScript != nil && hasScript(entry.Name)

		assessment := AnalyzeSkillEntry(entry, hs)

		// Run policy checks
		violations := CheckPolicyFromEntry(entry, hs, policy)
		assessment.Violations = violations

		for _, v := range violations {
			switch v.Severity {
			case "error":
				totalErrors++
			case "warning":
				totalWarnings++
			}
		}

		totalScore += assessment.Score.Value
		report.Assessments = append(report.Assessments, assessment)
	}

	// Compute aggregate score as average
	avgScore := 0
	if len(entries) > 0 {
		avgScore = totalScore / len(entries)
	}
	report.AggregateScore = RiskScore{
		Value: avgScore,
		Level: classifyScore(avgScore),
	}

	report.PolicySummary = PolicySummary{
		TotalViolations: totalErrors + totalWarnings,
		Errors:          totalErrors,
		Warnings:        totalWarnings,
		Passed:          totalErrors == 0,
	}

	return report
}

// FormatJSON serializes an AuditReport to indented JSON.
func FormatJSON(report *AuditReport) ([]byte, error) {
	return json.MarshalIndent(report, "", "  ")
}

// FormatText produces a human-readable text representation of an AuditReport.
func FormatText(report *AuditReport) string {
	var b strings.Builder

	b.WriteString("Security Audit Report\n")
	b.WriteString("=====================\n")
	fmt.Fprintf(&b, "Skills analyzed: %d\n", report.SkillCount)

	for _, a := range report.Assessments {
		b.WriteString("\n")
		fmt.Fprintf(&b, "%-28s Risk: %s (%d/100)\n", a.SkillName, a.Score.Level, a.Score.Value)

		if len(a.Factors) > 0 {
			b.WriteString("  Factors:\n")
			for _, f := range a.Factors {
				fmt.Fprintf(&b, "    %-8s +%-3d %s\n", f.Category, f.Points, f.Description)
			}
		}

		if len(a.Violations) > 0 {
			b.WriteString("  Violations:\n")
			for _, v := range a.Violations {
				sev := "WARN "
				if v.Severity == "error" {
					sev = "ERROR"
				}
				fmt.Fprintf(&b, "    %s %s: %s\n", sev, v.Rule, v.Message)
			}
		} else {
			b.WriteString("  Violations: none\n")
		}

		if len(a.Recommendations) > 0 {
			b.WriteString("  Recommendations:\n")
			for _, r := range a.Recommendations {
				fmt.Fprintf(&b, "    - %s\n", r)
			}
		}
	}

	b.WriteString("\n")
	fmt.Fprintf(&b, "Aggregate Risk: %s (%d/100)\n", report.AggregateScore.Level, report.AggregateScore.Value)
	passedStr := "PASSED"
	if !report.PolicySummary.Passed {
		passedStr = "FAILED"
	}
	fmt.Fprintf(&b, "Policy Summary: %s (%d errors, %d warnings)\n",
		passedStr, report.PolicySummary.Errors, report.PolicySummary.Warnings)

	return b.String()
}

func toAnySlice(ss []string) []any {
	result := make([]any, len(ss))
	for i, s := range ss {
		result[i] = s
	}
	return result
}
