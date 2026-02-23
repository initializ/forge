package build

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-skills/contract"
)

func TestSecurityAnalysisStage_Name(t *testing.T) {
	s := &SecurityAnalysisStage{}
	if s.Name() != "security-analysis" {
		t.Fatalf("expected name 'security-analysis', got %q", s.Name())
	}
}

func TestSecurityAnalysisStage_SkipNoSkills(t *testing.T) {
	s := &SecurityAnalysisStage{}
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{})

	err := s.Execute(context.Background(), bc)
	if err != nil {
		t.Fatalf("expected no error when no skills, got: %v", err)
	}
}

func TestSecurityAnalysisStage_CleanSkills(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "output")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatal(err)
	}

	s := &SecurityAnalysisStage{}
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{
		WorkDir:   tmpDir,
		OutputDir: outDir,
	})
	bc.SkillEntries = []contract.SkillEntry{
		{
			Name: "simple-tool",
			ForgeReqs: &contract.SkillRequirements{
				Bins: []string{"curl"},
				Env:  &contract.EnvRequirements{Required: []string{"API_KEY"}},
			},
		},
	}

	err := s.Execute(context.Background(), bc)
	if err != nil {
		t.Fatalf("expected no error for clean skills, got: %v", err)
	}

	// Check artifact was written
	auditPath := filepath.Join(outDir, "compiled", "security-audit.json")
	if _, statErr := os.Stat(auditPath); os.IsNotExist(statErr) {
		t.Fatal("security-audit.json not written")
	}

	// Check it was recorded in generated files
	if _, ok := bc.GeneratedFiles["compiled/security-audit.json"]; !ok {
		t.Fatal("audit file not recorded in generated files")
	}

	// Check SecurityAudit was set
	if bc.SecurityAudit == nil {
		t.Fatal("SecurityAudit not set in build context")
	}
}

func TestSecurityAnalysisStage_PolicyFail(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "output")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatal(err)
	}

	s := &SecurityAnalysisStage{}
	bc := pipeline.NewBuildContext(pipeline.PipelineOptions{
		WorkDir:   tmpDir,
		OutputDir: outDir,
	})
	bc.SkillEntries = []contract.SkillEntry{
		{
			Name: "danger-tool",
			ForgeReqs: &contract.SkillRequirements{
				Bins: []string{"nc"}, // denied by default policy
			},
		},
	}

	err := s.Execute(context.Background(), bc)
	if err == nil {
		t.Fatal("expected error for policy-violating skills")
	}
}
