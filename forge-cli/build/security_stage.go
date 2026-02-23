package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-skills/analyzer"
	"github.com/initializ/forge/forge-skills/contract"
)

// SecurityAnalysisStage runs security risk analysis and policy checks on skills.
type SecurityAnalysisStage struct{}

func (s *SecurityAnalysisStage) Name() string { return "security-analysis" }

func (s *SecurityAnalysisStage) Execute(ctx context.Context, bc *pipeline.BuildContext) error {
	// Skip if no skills were parsed
	if bc.SkillEntries == nil {
		return nil
	}

	entries, ok := bc.SkillEntries.([]contract.SkillEntry)
	if !ok || len(entries) == 0 {
		return nil
	}

	// Build a hasScript checker from the filesystem
	skillsDir := filepath.Join(bc.Opts.WorkDir, "skills")
	hasScript := func(name string) bool {
		scriptPath := filepath.Join(skillsDir, "scripts", name+".sh")
		_, err := os.Stat(scriptPath)
		return err == nil
	}

	policy := analyzer.DefaultPolicy()
	report := analyzer.GenerateReportFromEntries(entries, hasScript, policy)
	bc.SecurityAudit = report

	// Write audit artifact
	auditJSON, err := analyzer.FormatJSON(report)
	if err != nil {
		return fmt.Errorf("formatting security audit: %w", err)
	}

	auditDir := filepath.Join(bc.Opts.OutputDir, "compiled")
	if err := os.MkdirAll(auditDir, 0755); err != nil {
		return fmt.Errorf("creating audit directory: %w", err)
	}

	auditPath := filepath.Join(auditDir, "security-audit.json")
	if err := os.WriteFile(auditPath, auditJSON, 0644); err != nil {
		return fmt.Errorf("writing security audit: %w", err)
	}
	bc.AddFile("compiled/security-audit.json", auditPath)

	// Add warnings for policy violations
	for _, a := range report.Assessments {
		for _, v := range a.Violations {
			bc.AddWarning(fmt.Sprintf("[%s] %s: %s", a.SkillName, v.Rule, v.Message))
		}
	}

	// Block build if policy has errors
	if !report.PolicySummary.Passed {
		return fmt.Errorf("security policy check failed: %d error(s), %d warning(s)",
			report.PolicySummary.Errors, report.PolicySummary.Warnings)
	}

	return nil
}
