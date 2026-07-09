package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	cliskills "github.com/initializ/forge/forge-cli/skills"
	"github.com/initializ/forge/forge-core/pipeline"
	"github.com/initializ/forge/forge-skills/contract"
	skillsparser "github.com/initializ/forge/forge-skills/parser"
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
		parsed, meta, parseErr := cliskills.ParseFileWithMetadata(skillsPath)
		if parseErr != nil {
			return fmt.Errorf("parsing skills file: %w", parseErr)
		}
		entries = synthesizeInstructional(parsed, meta)
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

	// Aggregate skill requirements and store in build context. Capabilities
	// count even with no bins/env: the browser capability drives a synthetic
	// chromium install in the requirements stage.
	reqs := requirements.AggregateRequirements(entries)
	if len(reqs.Bins) > 0 || len(reqs.EnvRequired) > 0 || len(reqs.EnvOneOf) > 0 || len(reqs.EnvOptional) > 0 || len(reqs.Capabilities) > 0 {
		bc.SkillRequirements = reqs
	}

	// We deliberately do not write compiled/prompt.txt or
	// compiled/skills/skills.json. The runtime re-globs skills/
	// SKILL.md on every startup (runner.discoverSkillFiles +
	// buildSkillCatalog) and never opens either file. Generating
	// them just bloated the build output and the container image
	// for no consumer benefit. See issue #147 for the trace.
	// External library consumers (forgecore.Compile) still get the
	// in-memory CompiledSkills struct directly.

	bc.SkillsCount = len(entries)
	if bc.Spec != nil {
		bc.Spec.SkillsSpecVersion = "agentskills-v1"
		bc.Spec.ForgeSkillsExtVersion = "1.0"
	}
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

		entries, meta, parseErr := cliskills.ParseFileWithMetadata(skillPath)
		if parseErr != nil {
			fmt.Fprintf(os.Stderr, "  [skills] warning: parsing %s: %v\n", skillPath, parseErr)
			continue
		}
		allEntries = append(allEntries, synthesizeInstructional(entries, meta)...)
	}
	return allEntries, nil
}

// synthesizeInstructional handles skills whose SKILL.md carries forge metadata
// (capabilities, egress_domains, guardrails) but declares no "## Tool:"
// entries — e.g. a capability-only browser skill. Without this the skill's
// requirements would be dropped from the build. Mirrors the runtime's
// validateSkillRequirements handling.
func synthesizeInstructional(entries []contract.SkillEntry, meta *contract.SkillMetadata) []contract.SkillEntry {
	if len(entries) > 0 || meta == nil || meta.Metadata["forge"] == nil {
		return entries
	}
	forgeReqs, _, _ := skillsparser.ExtractForgeReqs(meta)
	return []contract.SkillEntry{{Name: meta.Name, Metadata: meta, ForgeReqs: forgeReqs}}
}
