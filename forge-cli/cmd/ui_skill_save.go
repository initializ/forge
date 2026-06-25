package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	forgeui "github.com/initializ/forge/forge-ui"
)

// SaveSkillToDisk writes a generated skill (SKILL.md plus any helper
// scripts) into the agent's skills/ directory and returns the same
// SkillSaveResult shape the dashboard's save endpoint emits.
//
// Edit-mode behavior (issue #193): when opts.Overwrite is true AND
// opts.EditingName matches opts.SkillName, the function removes the
// existing scripts/ directory before writing the new set. Without this
// step, scripts the user dropped during the edit would linger on disk
// — the rewritten SKILL.md would reference some-other-script.sh while
// the old, no-longer-listed scripts continued to be discovered. The
// EditingName==SkillName guard is defense in depth — the calling
// handler already rejects overwrite requests where the names don't
// match, but mirroring it here keeps this function safe to call from
// any future caller too.
//
// This function is lifted out of ui.go's anonymous SkillSaveFunc so
// it's directly unit-testable.
func SaveSkillToDisk(opts forgeui.SkillSaveOptions) (*forgeui.SkillSaveResult, error) {
	skillDir := filepath.Join(opts.AgentDir, "skills", opts.SkillName)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating skill directory: %w", err)
	}

	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(opts.SkillMD), 0o644); err != nil {
		return nil, fmt.Errorf("writing SKILL.md: %w", err)
	}

	scriptsDir := filepath.Join(skillDir, "scripts")
	if opts.Overwrite && opts.EditingName == opts.SkillName {
		if err := os.RemoveAll(scriptsDir); err != nil {
			return nil, fmt.Errorf("clearing stale scripts: %w", err)
		}
	}
	if len(opts.Scripts) > 0 {
		if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
			return nil, fmt.Errorf("creating scripts directory: %w", err)
		}
		for filename, content := range opts.Scripts {
			scriptPath := filepath.Join(scriptsDir, filename)
			if err := os.WriteFile(scriptPath, []byte(content), 0o755); err != nil {
				return nil, fmt.Errorf("writing script %s: %w", filename, err)
			}
		}
	}

	result := &forgeui.SkillSaveResult{
		Path: "skills/" + opts.SkillName + "/SKILL.md",
	}

	reqInfo := ParseSkillRequirements(opts.SkillMD)

	if len(opts.EnvVars) > 0 {
		written, _ := AppendEnvVars(opts.AgentDir, opts.EnvVars, opts.SkillName)
		result.EnvConfigured = written
	}

	if len(reqInfo.EgressDomains) > 0 {
		added, _ := MergeEgressDomains(opts.AgentDir, reqInfo.EgressDomains)
		result.EgressAdded = added
	}

	if reqInfo.EnvReqs != nil {
		result.EnvMissing = CheckMissingEnv(opts.AgentDir, reqInfo.EnvReqs)
	}

	return result, nil
}
