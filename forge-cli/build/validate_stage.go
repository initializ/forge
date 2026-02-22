package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-core/validate"
)

// ValidateStage validates the generated output files.
type ValidateStage struct{}

func (s *ValidateStage) Name() string { return "validate-output" }

func (s *ValidateStage) Execute(ctx context.Context, bc *pipeline.BuildContext) error {
	agentJSON := filepath.Join(bc.Opts.OutputDir, "agent.json")
	data, err := os.ReadFile(agentJSON)
	if err != nil {
		return fmt.Errorf("reading agent.json for validation: %w", err)
	}

	errs, err := validate.ValidateAgentSpec(data)
	if err != nil {
		return fmt.Errorf("validating agent.json: %w", err)
	}
	if len(errs) > 0 {
		return fmt.Errorf("agent.json validation failed: %v", errs)
	}

	requiredFiles := []string{"agent.json", "Dockerfile"}
	for _, f := range requiredFiles {
		path := filepath.Join(bc.Opts.OutputDir, f)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return fmt.Errorf("expected output file missing: %s", f)
		}
	}

	// Validate skills artifacts if skills were compiled
	if bc.SkillsCount > 0 {
		skillsArtifacts := []string{
			filepath.Join("compiled", "skills", "skills.json"),
			filepath.Join("compiled", "prompt.txt"),
		}
		for _, f := range skillsArtifacts {
			path := filepath.Join(bc.Opts.OutputDir, f)
			if _, err := os.Stat(path); os.IsNotExist(err) {
				return fmt.Errorf("expected skills artifact missing: %s", f)
			}
		}
	}

	// Validate egress artifact if egress was resolved
	if bc.EgressResolved != nil {
		egressPath := filepath.Join(bc.Opts.OutputDir, "compiled", "egress_allowlist.json")
		if _, err := os.Stat(egressPath); os.IsNotExist(err) {
			return fmt.Errorf("expected egress artifact missing: compiled/egress_allowlist.json")
		}
	}

	return nil
}
