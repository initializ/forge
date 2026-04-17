package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	cliskills "github.com/initializ/forge/forge-cli/skills"
	"github.com/initializ/forge/forge-core/pipeline"
	skillcompiler "github.com/initializ/forge/forge-skills/compiler"
	"github.com/initializ/forge/forge-skills/contract"
	"github.com/initializ/forge/forge-skills/requirements"
)

// SkillsStage compiles SKILL.md into container artifacts.
type SkillsStage struct{}

func (s *SkillsStage) Name() string { return "compile-skills" }

func (s *SkillsStage) Execute(ctx context.Context, bc *pipeline.BuildContext) error {
	// Determine skills file path
	skillsPath := bc.Config.Skills.Path
	if skillsPath == "" {
		skillsPath = "SKILL.md"
	}
	if !filepath.IsAbs(skillsPath) {
		skillsPath = filepath.Join(bc.Opts.WorkDir, skillsPath)
	}

	// Parse root skills file if it exists
	var entries []contract.SkillEntry
	if _, err := os.Stat(skillsPath); err == nil {
		parsed, _, parseErr := cliskills.ParseFileWithMetadata(skillsPath)
		if parseErr != nil {
			return fmt.Errorf("parsing skills file: %w", parseErr)
		}
		entries = parsed
	}

	// Always scan skills/ subdirectory (skills may exist without root SKILL.md)
	skillsSubDir := filepath.Join(bc.Opts.WorkDir, "skills")
	subEntries, subErr := scanSkillsSubDir(skillsSubDir)
	if subErr != nil {
		fmt.Fprintf(os.Stderr, "  [skills] warning: scanning skills/ subdirectory: %v\n", subErr)
	}
	if len(subEntries) > 0 {
		entries = append(entries, subEntries...)
	}

	if len(entries) == 0 {
		return nil
	}

	// Store entries for downstream stages (e.g. security analysis)
	bc.SkillEntries = entries

	// Aggregate skill requirements and store in build context
	reqs := requirements.AggregateRequirements(entries)
	if len(reqs.Bins) > 0 || len(reqs.EnvRequired) > 0 || len(reqs.EnvOneOf) > 0 || len(reqs.EnvOptional) > 0 {
		bc.SkillRequirements = reqs
	}

	compiled, err := skillcompiler.Compile(entries)
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

// scanSkillsSubDir scans the skills/ subdirectory for SKILL.md files in each
// child directory and returns parsed entries merged from all discovered skills.
func scanSkillsSubDir(skillsDir string) ([]contract.SkillEntry, error) {
	info, err := os.Stat(skillsDir)
	if err != nil || !info.IsDir() {
		return nil, nil // skills/ directory does not exist, nothing to scan
	}

	dirEntries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil, fmt.Errorf("reading skills directory: %w", err)
	}

	var allEntries []contract.SkillEntry
	for _, de := range dirEntries {
		if !de.IsDir() {
			continue
		}
		skillPath := filepath.Join(skillsDir, de.Name(), "SKILL.md")
		if _, statErr := os.Stat(skillPath); os.IsNotExist(statErr) {
			continue
		}

		entries, _, parseErr := cliskills.ParseFileWithMetadata(skillPath)
		if parseErr != nil {
			fmt.Fprintf(os.Stderr, "  [skills] warning: parsing %s: %v\n", skillPath, parseErr)
			continue
		}
		allEntries = append(allEntries, entries...)
	}
	return allEntries, nil
}
