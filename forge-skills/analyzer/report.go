package analyzer

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/initializ/forge/forge-skills/contract"
)

// GenerateReport produces a full audit report from a SkillRegistry.
//
// Registry skills carry fully-typed SkillDescriptors (capabilities, trust
// hints, deny_output presence populated by the scanner), so this path
// analyzes descriptors directly rather than round-tripping through entries,
// which would drop those fields.
func GenerateReport(registry contract.SkillRegistry, policy SecurityPolicy) (*AuditReport, error) {
	skills, err := registry.List()
	if err != nil {
		return nil, fmt.Errorf("listing skills: %w", err)
	}

	report := newReport(len(skills))
	acc := &reportAccumulator{}
	for i := range skills {
		sd := &skills[i]
		hs := registry.HasScript(sd.Name)
		assessment := AnalyzeSkillDescriptor(sd, hs, policy)
		assessment.Violations = CheckPolicy(sd, hs, policy)
		acc.add(report, assessment)
	}
	acc.finalize(report, len(skills))
	return report, nil
}

// GenerateReportFromEntries produces an audit report from parsed skill entries.
func GenerateReportFromEntries(entries []contract.SkillEntry, hasScript func(string) bool, policy SecurityPolicy) *AuditReport {
	report := newReport(len(entries))
	acc := &reportAccumulator{}
	for i := range entries {
		entry := &entries[i]
		hs := hasScript != nil && hasScript(entry.Name)
		assessment := AnalyzeSkillEntry(entry, hs, policy)
		assessment.Violations = CheckPolicyFromEntry(entry, hs, policy)
		acc.add(report, assessment)
	}
	acc.finalize(report, len(entries))
	return report
}

// reportAccumulator carries running totals while assessments are added.
type reportAccumulator struct {
	totalScore    int
	totalErrors   int
	totalWarnings int
}

func newReport(n int) *AuditReport {
	return &AuditReport{
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		SkillCount:  n,
		Assessments: make([]SkillRiskAssessment, 0, n),
	}
}

// add appends an assessment and folds its violations into the running totals.
// "critical" counts as a failing severity alongside "error".
func (acc *reportAccumulator) add(report *AuditReport, assessment SkillRiskAssessment) {
	for _, v := range assessment.Violations {
		switch v.Severity {
		case "error", "critical":
			acc.totalErrors++
		case "warning":
			acc.totalWarnings++
		}
	}
	acc.totalScore += assessment.Score.Value
	report.Assessments = append(report.Assessments, assessment)
}

func (acc *reportAccumulator) finalize(report *AuditReport, n int) {
	avgScore := 0
	if n > 0 {
		avgScore = acc.totalScore / n
	}
	report.AggregateScore = RiskScore{Value: avgScore, Level: classifyScore(avgScore)}
	report.PolicySummary = PolicySummary{
		TotalViolations: acc.totalErrors + acc.totalWarnings,
		Errors:          acc.totalErrors,
		Warnings:        acc.totalWarnings,
		Passed:          acc.totalErrors == 0,
	}
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
				switch v.Severity {
				case "error":
					sev = "ERROR"
				case "critical":
					sev = "CRIT "
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
