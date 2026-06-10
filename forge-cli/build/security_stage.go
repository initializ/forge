package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-skills/analyzer"
	"github.com/initializ/forge/forge-skills/contract"
)

// SecurityAnalysisStage runs security risk analysis and policy checks on skills.
//
// The active SecurityPolicy is resolved with this precedence (highest
// wins): PolicyPathOverride (set by the `--policy` CLI flag) >
// bc.Config.Security.PolicyPath (forge.yaml) > analyzer.DefaultPolicy().
// When a policy file is loaded, its origin is printed to stderr so
// operators can see which knobs are active.
type SecurityAnalysisStage struct {
	// PolicyPathOverride is the value passed via `forge build
	// --policy`. Empty means "fall back to forge.yaml's
	// security.policy_path, then DefaultPolicy()".
	PolicyPathOverride string
}

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

	policy, policySource, err := s.resolvePolicy(bc)
	if err != nil {
		return err
	}
	if policySource != "" {
		fmt.Fprintf(os.Stderr, "security: using policy from %s\n", policySource)
	}

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
		printPolicyDiagnostics(report, auditPath, policySource)
		return fmt.Errorf("security policy check failed: %d error(s), %d warning(s) — see stderr above + %s",
			report.PolicySummary.Errors, report.PolicySummary.Warnings, auditPath)
	}

	return nil
}

// resolvePolicy returns the active policy and a human-readable source
// label (empty when DefaultPolicy is used). The CLI flag overrides
// forge.yaml; both are resolved relative to the forge.yaml directory
// when relative.
func (s *SecurityAnalysisStage) resolvePolicy(bc *pipeline.BuildContext) (analyzer.SecurityPolicy, string, error) {
	path := s.PolicyPathOverride
	source := ""
	if path == "" && bc.Config != nil && bc.Config.Security.PolicyPath != "" {
		path = bc.Config.Security.PolicyPath
		source = "forge.yaml security.policy_path"
	} else if path != "" {
		source = "--policy flag"
	}
	if path == "" {
		return analyzer.DefaultPolicy(), "", nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(bc.Opts.WorkDir, path)
	}
	loaded, err := analyzer.LoadPolicyFromFile(path)
	if err != nil {
		return analyzer.SecurityPolicy{}, "", fmt.Errorf("loading security policy: %w", err)
	}
	return loaded, fmt.Sprintf("%s (%s)", source, path), nil
}

// printPolicyDiagnostics emits per-skill violation detail + remediation
// hints to stderr so a build that fails on policy isn't reduced to "2
// error(s)" with the actionable detail buried in a JSON artifact.
func printPolicyDiagnostics(report *analyzer.AuditReport, auditPath, policySource string) {
	var b strings.Builder
	b.WriteString("security: policy check failed\n")
	for _, a := range report.Assessments {
		if len(a.Violations) == 0 {
			continue
		}
		fmt.Fprintf(&b, "  skill %q (risk score %d/%s):\n", a.SkillName, a.Score.Value, a.Score.Level)
		for _, v := range a.Violations {
			fmt.Fprintf(&b, "    - [%s] %s: %s\n", v.Severity, v.Rule, v.Message)
		}
		if len(a.Recommendations) > 0 {
			b.WriteString("    recommendations:\n")
			for _, r := range a.Recommendations {
				fmt.Fprintf(&b, "      • %s\n", r)
			}
		}
	}
	fmt.Fprintf(&b, "  full report: %s\n", auditPath)
	if policySource == "" {
		b.WriteString("  policy: builtin DefaultPolicy (override via security.policy_path in forge.yaml or `forge build --policy=path.yaml`)\n")
		b.WriteString("  to inspect: forge skills audit --format=text\n")
	} else {
		fmt.Fprintf(&b, "  policy: %s\n", policySource)
		b.WriteString("  to inspect: forge skills audit --policy=<same-file> --format=text\n")
	}
	fmt.Fprint(os.Stderr, b.String())
}
