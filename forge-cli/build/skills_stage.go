package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	coreskills "github.com/initializ/forge/forge-core/skills"
	"github.com/initializ/forge/forge-core/pipeline"
	cliskills "github.com/initializ/forge/forge-cli/skills"
)

// SkillsStage compiles skills.md into container artifacts.
type SkillsStage struct{}

func (s *SkillsStage) Name() string { return "compile-skills" }

func (s *SkillsStage) Execute(ctx context.Context, bc *pipeline.BuildContext) error {
	// Determine skills file path
	skillsPath := bc.Config.Skills.Path
	if skillsPath == "" {
		skillsPath = "skills.md"
	}
	if !filepath.IsAbs(skillsPath) {
		skillsPath = filepath.Join(bc.Opts.WorkDir, skillsPath)
	}

	// Skip silently if not found
	if _, err := os.Stat(skillsPath); os.IsNotExist(err) {
		return nil
	}

	entries, _, err := cliskills.ParseFileWithMetadata(skillsPath)
	if err != nil {
		return fmt.Errorf("parsing skills file: %w", err)
	}

	if len(entries) == 0 {
		return nil
	}

	// Aggregate skill requirements and store in build context
	reqs := coreskills.AggregateRequirements(entries)
	if len(reqs.Bins) > 0 || len(reqs.EnvRequired) > 0 || len(reqs.EnvOneOf) > 0 || len(reqs.EnvOptional) > 0 {
		bc.SkillRequirements = reqs
	}

	compiled, err := coreskills.Compile(entries)
	if err != nil {
		return fmt.Errorf("compiling skills: %w", err)
	}

	if err := cliskills.WriteArtifacts(bc.Opts.OutputDir, compiled); err != nil {
		return fmt.Errorf("writing skills artifacts: %w", err)
	}

	bc.SkillsCount = compiled.Count
	if bc.Spec != nil {
		bc.Spec.SkillsSpecVersion = "agentskills-v1"
		bc.Spec.ForgeSkillsExtVersion = "1.0"
	}

	bc.AddFile("compiled/skills/skills.json", filepath.Join(bc.Opts.OutputDir, "compiled", "skills", "skills.json"))
	bc.AddFile("compiled/prompt.txt", filepath.Join(bc.Opts.OutputDir, "compiled", "prompt.txt"))
	return nil
}
