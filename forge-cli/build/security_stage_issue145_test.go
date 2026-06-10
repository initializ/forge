package build

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-core/types"
	"github.com/initializ/forge/forge-skills/contract"
)

// TestSecurityAnalysisStage_PolicyOverride_FromConfig confirms that a
// SecurityPolicy file pointed to by forge.yaml's
// `security.policy_path:` is loaded instead of DefaultPolicy(). The
// fixture policy here loosens MaxRiskScore so a synthetically
// high-score skill passes, proving the override is what's gating the
// build (not the builtin default).
func TestSecurityAnalysisStage_PolicyOverride_FromConfig(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "output")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(tmpDir, "policy.yaml")
	if err := os.WriteFile(policyPath, []byte("max_risk_score: 100\nscript_policy: allow\n"), 0644); err != nil {
		t.Fatal(err)
	}

	s := &SecurityAnalysisStage{}
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{WorkDir: tmpDir, OutputDir: outDir})
	bc.Config = &types.ForgeConfig{Security: types.SecurityConfig{PolicyPath: "policy.yaml"}}
	bc.SkillEntries = []contract.SkillEntry{noisySkillEntry()}

	if err := s.Execute(context.Background(), bc); err != nil {
		t.Fatalf("policy override should let noisy skill pass; got: %v", err)
	}
}

// TestSecurityAnalysisStage_PolicyOverride_FromCLIFlag confirms the
// `forge build --policy` flag (PolicyPathOverride) wins over the
// forge.yaml field. Setting forge.yaml's policy_path to a missing file
// while passing a valid override path proves the override is read
// first.
func TestSecurityAnalysisStage_PolicyOverride_FromCLIFlag(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "output")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatal(err)
	}
	overridePath := filepath.Join(tmpDir, "override.yaml")
	if err := os.WriteFile(overridePath, []byte("max_risk_score: 100\nscript_policy: allow\n"), 0644); err != nil {
		t.Fatal(err)
	}

	s := &SecurityAnalysisStage{PolicyPathOverride: overridePath}
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{WorkDir: tmpDir, OutputDir: outDir})
	bc.Config = &types.ForgeConfig{
		Security: types.SecurityConfig{PolicyPath: filepath.Join(tmpDir, "nonexistent.yaml")},
	}
	bc.SkillEntries = []contract.SkillEntry{noisySkillEntry()}

	if err := s.Execute(context.Background(), bc); err != nil {
		t.Fatalf("CLI flag should win over forge.yaml; got: %v", err)
	}
}

// TestSecurityAnalysisStage_PolicyOverride_MissingFile surfaces a
// load error rather than silently falling back to DefaultPolicy. An
// operator who pointed at a typoed file would otherwise be lulled
// into believing their custom policy was applied.
func TestSecurityAnalysisStage_PolicyOverride_MissingFile(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "output")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatal(err)
	}

	s := &SecurityAnalysisStage{PolicyPathOverride: filepath.Join(tmpDir, "missing.yaml")}
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{WorkDir: tmpDir, OutputDir: outDir})
	bc.SkillEntries = []contract.SkillEntry{{Name: "noop"}}

	err := s.Execute(context.Background(), bc)
	if err == nil || !strings.Contains(err.Error(), "loading security policy") {
		t.Fatalf("expected load error when policy file missing, got %v", err)
	}
}

// TestSecurityAnalysisStage_PolicyFail_ErrorIncludesArtifactPath
// pins the user-facing improvement from issue #145: the build error
// message references the audit JSON path so operators can find the
// per-violation detail instead of staring at a bare "2 error(s)".
func TestSecurityAnalysisStage_PolicyFail_ErrorIncludesArtifactPath(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "output")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatal(err)
	}

	s := &SecurityAnalysisStage{}
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{WorkDir: tmpDir, OutputDir: outDir})
	bc.SkillEntries = []contract.SkillEntry{
		{
			Name: "danger-tool",
			ForgeReqs: &contract.SkillRequirements{
				Bins: []contract.BinRequirement{{Name: "nc"}},
			},
		},
	}

	err := s.Execute(context.Background(), bc)
	if err == nil {
		t.Fatal("expected error for denied-binary skill")
	}
	auditPath := filepath.Join(outDir, "compiled", "security-audit.json")
	if !strings.Contains(err.Error(), auditPath) {
		t.Errorf("error message must reference the audit JSON path so operators can find per-violation detail; got: %q", err.Error())
	}
}

// noisySkillEntry builds a skill entry that lands above the default
// MaxRiskScore so override tests can assert "the override is what let
// this through". Shaped to mirror the bundled code-review skill plus
// a high-risk binary, which pushes the score past DefaultPolicy=90.
func noisySkillEntry() contract.SkillEntry {
	return contract.SkillEntry{
		Name: "noisy",
		ForgeReqs: &contract.SkillRequirements{
			Bins: []contract.BinRequirement{
				{Name: "python"}, // high-risk binary → +15
				{Name: "bash"},   // high-risk binary → +15
				{Name: "curl"},
				{Name: "jq"},
			},
			Env: &contract.EnvRequirements{
				Required: []string{"VAR1", "VAR2", "VAR3"},
				Optional: []string{"VAR4", "VAR5", "VAR6", "VAR7", "VAR8"},
			},
		},
		Metadata: &contract.SkillMetadata{
			Metadata: map[string]map[string]any{
				"forge": {
					"egress_domains": []any{
						"some-unknown-1.example.com",
						"some-unknown-2.example.com",
						"some-unknown-3.example.com",
						"some-unknown-4.example.com",
						"some-unknown-5.example.com",
						"some-unknown-6.example.com",
					},
				},
			},
		},
	}
}
